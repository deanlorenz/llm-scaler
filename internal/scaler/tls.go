package scaler

import (
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"os"
)

// NewTLSConfig builds the server-side TLS configuration for the external
// scaler.
//
// getCertificate serves the scaler's own certificate; pass a
// certwatcher.CertWatcher's GetCertificate so a rotated Secret is picked up
// without a restart. clientCAFile, when non-empty, turns on mutual TLS: every
// client must present a certificate that chains to a CA in that PEM bundle,
// and one that does not is refused during the handshake, before any RPC -- and
// so before any call can register a workload or read a decision. KEDA presents
// one when the trigger's TriggerAuthentication carries tlsClientCert and
// tlsClientKey. The bundle is read once; rotating it needs a restart.
func NewTLSConfig(getCertificate func(*tls.ClientHelloInfo) (*tls.Certificate, error), clientCAFile string) (*tls.Config, error) {
	if getCertificate == nil {
		return nil, errors.New("external scaler TLS needs a serving certificate")
	}
	cfg := &tls.Config{
		MinVersion:     tls.VersionTLS12,
		GetCertificate: getCertificate,
	}
	if clientCAFile == "" {
		return cfg, nil
	}
	pem, err := os.ReadFile(clientCAFile)
	if err != nil {
		return nil, fmt.Errorf("reading external scaler client CA %q: %w", clientCAFile, err)
	}
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pem) {
		return nil, fmt.Errorf("external scaler client CA %q holds no PEM certificate", clientCAFile)
	}
	cfg.ClientCAs = pool
	cfg.ClientAuth = tls.RequireAndVerifyClientCert
	return cfg, nil
}
