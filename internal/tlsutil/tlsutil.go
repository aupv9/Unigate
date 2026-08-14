// Package tlsutil builds a *tls.Config for the service's listeners
// from config.TLSConfig, supporting both server-side TLS and mutual
// TLS (NFR5).
package tlsutil

import (
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"

	"github.com/aupv9/unigate/internal/config"
)

// Build returns nil (no error) when cfg.Enabled is false, meaning the
// caller should serve plaintext - matching a deployment where TLS is
// terminated upstream by a mesh/sidecar instead.
func Build(cfg config.TLSConfig) (*tls.Config, error) {
	if !cfg.Enabled {
		return nil, nil
	}

	cert, err := tls.LoadX509KeyPair(cfg.CertFile, cfg.KeyFile)
	if err != nil {
		return nil, fmt.Errorf("tlsutil: load server keypair: %w", err)
	}

	tlsCfg := &tls.Config{
		MinVersion:   tls.VersionTLS12,
		Certificates: []tls.Certificate{cert},
	}

	if cfg.RequireClientCert {
		caBytes, err := os.ReadFile(cfg.ClientCAFile)
		if err != nil {
			return nil, fmt.Errorf("tlsutil: read client CA file: %w", err)
		}
		pool := x509.NewCertPool()
		if !pool.AppendCertsFromPEM(caBytes) {
			return nil, fmt.Errorf("tlsutil: no valid certificates found in %s", cfg.ClientCAFile)
		}
		tlsCfg.ClientCAs = pool
		tlsCfg.ClientAuth = tls.RequireAndVerifyClientCert
	}

	return tlsCfg, nil
}
