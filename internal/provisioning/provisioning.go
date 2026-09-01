// Package provisioning is the claim-flow client for native Go edge
// clients (cmd/hostagent now, cmd/fleet in Phase 7): exchange a one-time
// registration token for broker credentials via POST /v1/devices/claim,
// then cache them locally so later runs skip claiming entirely. This is
// the Go-process equivalent of what the PWA does with localStorage.
package provisioning

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"time"

	"github.com/google/uuid"

	"github.com/Ritesh956/SenseGrid/internal/tlsutil"
)

// Credentials is a claimed device's identity and broker credential.
type Credentials struct {
	DeviceID     string `json:"device_id"`
	Name         string `json:"name"`
	Type         string `json:"type"`
	MQTTUsername string `json:"mqtt_username"`
	MQTTPassword string `json:"mqtt_password"`
}

// LoadOrClaim returns cached credentials from stateFile if present;
// otherwise it claims a new device against controlURL using token and
// caches the result at stateFile for next time. token may be empty only
// when stateFile already holds valid credentials.
func LoadOrClaim(ctx context.Context, stateFile, controlURL, token, caFile string) (Credentials, error) {
	if creds, err := load(stateFile); err == nil {
		return creds, nil
	}
	if token == "" {
		return Credentials{}, fmt.Errorf("provisioning: no cached credentials at %s and no claim token was given", stateFile)
	}

	creds, err := claim(ctx, controlURL, token, caFile)
	if err != nil {
		return Credentials{}, err
	}
	if err := save(stateFile, creds); err != nil {
		return Credentials{}, fmt.Errorf("provisioning: claimed device %s but failed to cache credentials: %w", creds.DeviceID, err)
	}
	return creds, nil
}

func claim(ctx context.Context, controlURL, token, caFile string) (Credentials, error) {
	body, err := json.Marshal(map[string]string{
		"token":     token,
		"device_id": uuid.NewString(),
	})
	if err != nil {
		return Credentials{}, err
	}

	httpClient := &http.Client{Timeout: 15 * time.Second}
	if caFile != "" {
		tlsCfg, err := tlsutil.FromCAFile(caFile)
		if err != nil {
			return Credentials{}, fmt.Errorf("provisioning: loading CA: %w", err)
		}
		httpClient.Transport = &http.Transport{TLSClientConfig: tlsCfg}
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, controlURL+"/v1/devices/claim", bytes.NewReader(body))
	if err != nil {
		return Credentials{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := httpClient.Do(req)
	if err != nil {
		return Credentials{}, fmt.Errorf("provisioning: claim request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	respBody, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return Credentials{}, fmt.Errorf("provisioning: claim rejected (%d): %s", resp.StatusCode, string(respBody))
	}

	var creds Credentials
	if err := json.Unmarshal(respBody, &creds); err != nil {
		return Credentials{}, fmt.Errorf("provisioning: decoding claim response: %w", err)
	}
	return creds, nil
}

func load(path string) (Credentials, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return Credentials{}, err
	}
	var creds Credentials
	if err := json.Unmarshal(raw, &creds); err != nil {
		return Credentials{}, err
	}
	return creds, nil
}

func save(path string, creds Credentials) error {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(creds, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, raw, 0o600)
}
