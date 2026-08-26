// Phase 4 JWT auth: three roles (admin/operator/viewer), HMAC-signed
// (HS256) with one shared signing key from JWT_SIGNING_KEY. Tokens are
// minted two ways: `control jwt create` (jwt_cli.go, no subject — shell
// access to the binary is the auth boundary) and, since Phase 5,
// POST /v1/auth/login (auth_login.go, subject = username, backed by
// internal/users). Both call signToken and produce tokens requireRole
// verifies identically — a login-minted token is not a different kind of
// token, just one with a subject and a different TTL.
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

// signToken mints a signed JWT for role, valid for ttl. subject is the
// username for a login-minted token (auth_login.go), or "" for a
// CLI-minted one (jwt_cli.go) — requireRole never looks at it; it exists
// purely so a login session's token can be traced back to who it belongs
// to (logs, future auditing), not as part of the auth decision itself.
func signToken(role, issuer string, ttl time.Duration, signingKey []byte, subject string) (string, error) {
	if _, ok := roleRank[role]; !ok {
		return "", fmt.Errorf("auth: unknown role %q", role)
	}
	now := time.Now()
	c := claims{
		Role: role,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   subject,
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

// verifyToken parses and validates a raw JWT string, returning its role
// and subject (subject is "" for a CLI-minted token — see signToken).
// Shared by verifyBearer (Authorization header, every REST endpoint) and
// the WS handshake (?token= query param, ws_handler.go — a browser
// WebSocket client can't set a custom Authorization header).
func verifyToken(tokenStr, issuer string, signingKey []byte) (role, subject string, err error) {
	if tokenStr == "" {
		return "", "", fmt.Errorf("auth: empty token")
	}
	var c claims
	_, err = jwt.ParseWithClaims(tokenStr, &c, func(*jwt.Token) (any, error) {
		return signingKey, nil
	}, jwt.WithValidMethods([]string{"HS256"}), jwt.WithIssuer(issuer))
	if err != nil {
		return "", "", fmt.Errorf("auth: invalid token: %w", err)
	}
	if _, ok := roleRank[c.Role]; !ok {
		return "", "", fmt.Errorf("auth: unknown role %q", c.Role)
	}
	return c.Role, c.Subject, nil
}

// verifyBearer parses and validates the Authorization: Bearer <token>
// header on r, returning the token's role.
func verifyBearer(r *http.Request, issuer string, signingKey []byte) (string, error) {
	header := r.Header.Get("Authorization")
	tokenStr, ok := strings.CutPrefix(header, "Bearer ")
	if !ok || tokenStr == "" {
		return "", fmt.Errorf("auth: missing or malformed Authorization header")
	}
	role, _, err := verifyToken(tokenStr, issuer, signingKey)
	return role, err
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
