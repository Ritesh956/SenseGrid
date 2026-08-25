// Phase 4 JWT auth: three roles (admin/operator/viewer), HMAC-signed
// (HS256) with one shared signing key from JWT_SIGNING_KEY. No login
// endpoint exists yet — tokens are minted by `control jwt create` (see
// jwt_cli.go), which reads the same signing key via the same
// config.Load(), so the CLI and the running server are always in sync.
// A real login endpoint is Phase 5's job, once the console needs one.
package main

import (
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/golang-jwt/jwt/v5"
)

// Roles, ordered lowest to highest privilege. Treated as a hierarchy
// (admin can do anything operator/viewer can) rather than disjoint scopes
// — the blueprint defines them by scope (admin: rollouts/config-writes;
// operator: ack/resolve alerts; viewer: read-only) but doesn't say whether
// they nest; a hierarchy is the simpler, more conventional RBAC choice for
// a single-operator project and is what roleAllows below implements.
const (
	roleViewer   = "viewer"
	roleOperator = "operator"
	roleAdmin    = "admin"
)

var roleRank = map[string]int{roleViewer: 0, roleOperator: 1, roleAdmin: 2}

// roleAllows reports whether a token carrying `have` satisfies a handler
// that requires at least `need`.
func roleAllows(have, need string) bool {
	haveRank, ok := roleRank[have]
	if !ok {
		return false
	}
	needRank, ok := roleRank[need]
	if !ok {
		return false
	}
	return haveRank >= needRank
}

type claims struct {
	Role string `json:"role"`
	jwt.RegisteredClaims
}

// signToken mints a signed JWT for role, valid for ttl.
func signToken(role, issuer string, ttl time.Duration, signingKey []byte) (string, error) {
	if _, ok := roleRank[role]; !ok {
		return "", fmt.Errorf("auth: unknown role %q", role)
	}
	now := time.Now()
	c := claims{
		Role: role,
		RegisteredClaims: jwt.RegisteredClaims{
			Issuer:    issuer,
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(now.Add(ttl)),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, c)
	signed, err := token.SignedString(signingKey)
	if err != nil {
		return "", fmt.Errorf("auth: signing token: %w", err)
	}
	return signed, nil
}

// verifyBearer parses and validates the Authorization: Bearer <token>
// header on r, returning the token's role.
func verifyBearer(r *http.Request, issuer string, signingKey []byte) (string, error) {
	header := r.Header.Get("Authorization")
	tokenStr, ok := strings.CutPrefix(header, "Bearer ")
	if !ok || tokenStr == "" {
		return "", fmt.Errorf("auth: missing or malformed Authorization header")
	}

	var c claims
	_, err := jwt.ParseWithClaims(tokenStr, &c, func(*jwt.Token) (any, error) {
		return signingKey, nil
	}, jwt.WithValidMethods([]string{"HS256"}), jwt.WithIssuer(issuer))
	if err != nil {
		return "", fmt.Errorf("auth: invalid token: %w", err)
	}
	if _, ok := roleRank[c.Role]; !ok {
		return "", fmt.Errorf("auth: unknown role %q", c.Role)
	}
	return c.Role, nil
}

// requireRole wraps handler so it only runs for a request bearing a valid
// JWT whose role satisfies minRole (see roleAllows) — 401 for a
// missing/invalid/expired token, 403 for an insufficient role.
func requireRole(minRole, issuer string, signingKey []byte, handler func(w http.ResponseWriter, r *http.Request, role string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		role, err := verifyBearer(r, issuer, signingKey)
		if err != nil {
			writeJSONError(w, http.StatusUnauthorized, "invalid or missing bearer token")
			return
		}
		if !roleAllows(role, minRole) {
			writeJSONError(w, http.StatusForbidden, "insufficient role")
			return
		}
		handler(w, r, role)
	}
}
