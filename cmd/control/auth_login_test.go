package main

import (
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/Ritesh956/SenseGrid/internal/users"
)

// TestLoginHandler_ValidationRejections covers the request-validation path
// of POST /v1/auth/login, which returns before ever touching the store —
// like internal/devices.Store and internal/alerts.Store, internal/users is
// a thin Postgres CRUD wrapper with no test-DB harness in this repo, so
// the credential-check path (right/wrong password, unknown username)
// isn't exercised here; users.New(nil) is safe precisely because these
// cases never reach store.GetByUsername.
func TestLoginHandler_ValidationRejections(t *testing.T) {
	mux := http.NewServeMux()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	registerAuthHandlers(mux, logger, users.New(nil), "sensegrid-control", []byte("test-signing-key"), time.Hour)

	cases := []struct {
		name string
		body string
	}{
		{"malformed json", "{not json"},
		{"missing username", `{"password":"x"}`},
		{"missing password", `{"username":"alice"}`},
		{"empty body", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := httptest.NewRequest(http.MethodPost, "/v1/auth/login", strings.NewReader(c.body))
			w := httptest.NewRecorder()
			mux.ServeHTTP(w, r)
			if w.Code != http.StatusBadRequest {
				t.Errorf("status = %d, want 400", w.Code)
			}
		})
	}
}
