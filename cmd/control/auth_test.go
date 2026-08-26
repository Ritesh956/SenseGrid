package main

import (
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRoleAllows(t *testing.T) {
	cases := []struct {
		have, need string
		want       bool
	}{
		{roleAdmin, roleAdmin, true},
		{roleAdmin, roleOperator, true},
		{roleAdmin, roleViewer, true},
		{roleOperator, roleAdmin, false},
		{roleOperator, roleOperator, true},
		{roleOperator, roleViewer, true},
		{roleViewer, roleOperator, false},
		{roleViewer, roleViewer, true},
		{"bogus", roleViewer, false},
	}
	for _, c := range cases {
		if got := roleAllows(c.have, c.need); got != c.want {
			t.Errorf("roleAllows(%q, %q) = %v, want %v", c.have, c.need, got, c.want)
		}
	}
}

func TestSignAndVerifyRoundTrip(t *testing.T) {
	key := []byte("test-signing-key")
	token, err := signToken(roleAdmin, "sensegrid-control", time.Hour, key, "")
	if err != nil {
		t.Fatal(err)
	}

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+token)
	role, err := verifyBearer(r, "sensegrid-control", key)
	if err != nil {
		t.Fatal(err)
	}
	if role != roleAdmin {
		t.Errorf("role = %q, want %q", role, roleAdmin)
	}
}

func TestVerifyToken_Subject(t *testing.T) {
	key := []byte("test-signing-key")

	token, err := signToken(roleOperator, "sensegrid-control", time.Hour, key, "alice")
	if err != nil {
		t.Fatal(err)
	}
	role, subject, err := verifyToken(token, "sensegrid-control", key)
	if err != nil {
		t.Fatal(err)
	}
	if role != roleOperator {
		t.Errorf("role = %q, want %q", role, roleOperator)
	}
	if subject != "alice" {
		t.Errorf("subject = %q, want %q", subject, "alice")
	}

	// CLI-minted tokens (jwt_cli.go) pass "" — verifyToken must round-trip
	// that as cleanly as a real username, since requireRole never looks at
	// subject at all.
	cliToken, err := signToken(roleAdmin, "sensegrid-control", time.Hour, key, "")
	if err != nil {
		t.Fatal(err)
	}
	if _, subject, err := verifyToken(cliToken, "sensegrid-control", key); err != nil || subject != "" {
		t.Errorf("subject = %q, err = %v, want \"\", nil", subject, err)
	}

	if _, _, err := verifyToken("", "sensegrid-control", key); err == nil {
		t.Error("empty token: expected an error, got nil")
	}
}

func TestVerifyBearer_Rejections(t *testing.T) {
	key := []byte("test-signing-key")
	wrongKey := []byte("wrong-signing-key")

	expired, err := signToken(roleViewer, "sensegrid-control", -time.Minute, key, "")
	if err != nil {
		t.Fatal(err)
	}
	wrongSig, err := signToken(roleViewer, "sensegrid-control", time.Hour, wrongKey, "")
	if err != nil {
		t.Fatal(err)
	}
	wrongIssuer, err := signToken(roleViewer, "someone-else", time.Hour, key, "")
	if err != nil {
		t.Fatal(err)
	}

	cases := []struct {
		name   string
		header string
	}{
		{"missing header", ""},
		{"not bearer scheme", "Basic abc123"},
		{"expired", "Bearer " + expired},
		{"wrong signing key", "Bearer " + wrongSig},
		{"wrong issuer", "Bearer " + wrongIssuer},
		{"garbage token", "Bearer not-a-jwt"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodGet, "/", nil)
			if c.header != "" {
				r.Header.Set("Authorization", c.header)
			}
			if _, err := verifyBearer(r, "sensegrid-control", key); err == nil {
				t.Error("expected an error, got nil")
			}
		})
	}
}

func TestRequireRole(t *testing.T) {
	key := []byte("test-signing-key")
	viewerToken, _ := signToken(roleViewer, "sensegrid-control", time.Hour, key, "")
	adminToken, _ := signToken(roleAdmin, "sensegrid-control", time.Hour, key, "")

	handler := requireRole(roleAdmin, "sensegrid-control", key, func(w http.ResponseWriter, r *http.Request, role string) {
		w.WriteHeader(http.StatusOK)
	})

	r := httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+viewerToken)
	w := httptest.NewRecorder()
	handler(w, r)
	if w.Code != http.StatusForbidden {
		t.Errorf("viewer against admin-required handler: status = %d, want 403", w.Code)
	}

	r = httptest.NewRequest(http.MethodGet, "/", nil)
	r.Header.Set("Authorization", "Bearer "+adminToken)
	w = httptest.NewRecorder()
	handler(w, r)
	if w.Code != http.StatusOK {
		t.Errorf("admin against admin-required handler: status = %d, want 200", w.Code)
	}

	r = httptest.NewRequest(http.MethodGet, "/", nil)
	w = httptest.NewRecorder()
	handler(w, r)
	if w.Code != http.StatusUnauthorized {
		t.Errorf("no token: status = %d, want 401", w.Code)
	}
}
