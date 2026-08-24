// Package tlsutil builds *tls.Config values that trust the SenseGrid dev
// CA, shared by every client that dials mosquitto or the control-plane API
// directly (internal/dynsec, internal/provisioning, and eventually
// cmd/fleet) instead of each reimplementing CA loading.
package tlsutil

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
)

// FromCAFile returns a tls.Config trusting the certificate(s) in caFile in
// addition to the system trust store.
func FromCAFile(caFile string) (*tls.Config, error) {
	pem, err := os.ReadFile(caFile)
	if err != nil {
		return nil, err
	}
	pool, err := x509.SystemCertPool()
	if err != nil || pool == nil {
		pool = x509.NewCertPool()
	}
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("tlsutil: no certificates found in %s", caFile)
	}
	return &tls.Config{RootCAs: pool, MinVersion: tls.VersionTLS12}, nil
}
