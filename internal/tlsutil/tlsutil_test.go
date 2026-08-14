package tlsutil

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/aupv9/unigate/internal/config"
)

// genCA creates a self-signed CA and returns its cert (PEM) and key.
func genCA(t *testing.T, cn string) (certPEM []byte, key *ecdsa.PrivateKey, cert *x509.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate CA key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: cn},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create CA cert: %v", err)
	}
	cert, err = x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse CA cert: %v", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	return certPEM, key, cert
}

// genLeaf creates a cert signed by the given CA, for either server or
// client use.
func genLeaf(t *testing.T, caCert *x509.Certificate, caKey *ecdsa.PrivateKey, cn string, isServer bool) (certPEM, keyPEM []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate leaf key: %v", err)
	}
	eku := x509.ExtKeyUsageClientAuth
	if isServer {
		eku = x509.ExtKeyUsageServerAuth
	}
	tmpl := &x509.Certificate{
		SerialNumber: big.NewInt(2),
		Subject:      pkix.Name{CommonName: cn},
		NotBefore:    time.Now().Add(-time.Hour),
		NotAfter:     time.Now().Add(time.Hour),
		KeyUsage:     x509.KeyUsageDigitalSignature,
		ExtKeyUsage:  []x509.ExtKeyUsage{eku},
		DNSNames:     []string{"127.0.0.1", "localhost"},
		IPAddresses:  []net.IP{net.ParseIP("127.0.0.1")},
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, caCert, &key.PublicKey, caKey)
	if err != nil {
		t.Fatalf("create leaf cert: %v", err)
	}
	certPEM = pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(key)
	if err != nil {
		t.Fatalf("marshal leaf key: %v", err)
	}
	keyPEM = pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})
	return certPEM, keyPEM
}

func writeFile(t *testing.T, dir, name string, data []byte) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatalf("write %s: %v", path, err)
	}
	return path
}

func TestBuild_DisabledReturnsNil(t *testing.T) {
	tlsCfg, err := Build(config.TLSConfig{Enabled: false})
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if tlsCfg != nil {
		t.Fatalf("expected nil tls.Config when disabled, got %+v", tlsCfg)
	}
}

func TestBuild_MissingCertFileErrors(t *testing.T) {
	_, err := Build(config.TLSConfig{Enabled: true, CertFile: "/nonexistent/cert.pem", KeyFile: "/nonexistent/key.pem"})
	if err == nil {
		t.Fatalf("expected error for missing cert file")
	}
}

func TestBuild_RequireClientCertMissingCAFileErrors(t *testing.T) {
	dir := t.TempDir()
	caCertPEM, caKey, caCert := genCA(t, "test-ca")
	serverCertPEM, serverKeyPEM := genLeaf(t, caCert, caKey, "server", true)
	_ = caCertPEM

	certPath := writeFile(t, dir, "server.crt", serverCertPEM)
	keyPath := writeFile(t, dir, "server.key", serverKeyPEM)

	_, err := Build(config.TLSConfig{
		Enabled: true, CertFile: certPath, KeyFile: keyPath,
		RequireClientCert: true, ClientCAFile: "/nonexistent/ca.pem",
	})
	if err == nil {
		t.Fatalf("expected error for missing client CA file")
	}
}

// TestMTLS_EndToEnd spins up a real TLS listener using tlsutil.Build's
// output and proves mTLS is actually enforced end-to-end: connections
// without a client cert, or with one from an untrusted CA, are
// rejected at the handshake; a cert signed by the configured CA is
// accepted.
func TestMTLS_EndToEnd(t *testing.T) {
	dir := t.TempDir()

	caCertPEM, caKey, caCert := genCA(t, "unigate-test-ca")
	serverCertPEM, serverKeyPEM := genLeaf(t, caCert, caKey, "server", true)
	clientCertPEM, clientKeyPEM := genLeaf(t, caCert, caKey, "trusted-client", false)

	otherCACertPEM, otherCAKey, otherCACert := genCA(t, "other-ca")
	untrustedClientCertPEM, untrustedClientKeyPEM := genLeaf(t, otherCACert, otherCAKey, "untrusted-client", false)
	_ = otherCACertPEM

	serverCertPath := writeFile(t, dir, "server.crt", serverCertPEM)
	serverKeyPath := writeFile(t, dir, "server.key", serverKeyPEM)
	caCertPath := writeFile(t, dir, "ca.crt", caCertPEM)

	serverTLSCfg, err := Build(config.TLSConfig{
		Enabled: true, CertFile: serverCertPath, KeyFile: serverKeyPath,
		RequireClientCert: true, ClientCAFile: caCertPath,
	})
	if err != nil {
		t.Fatalf("build server tls config: %v", err)
	}

	lis, err := tls.Listen("tcp", "127.0.0.1:0", serverTLSCfg)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer lis.Close()

	go func() {
		for {
			conn, err := lis.Accept()
			if err != nil {
				return
			}
			// TLS 1.3 quirk: the client's Handshake() can return
			// successfully before it learns the server rejected its
			// (missing/invalid) certificate - that failure only
			// surfaces on a subsequent read/write. So the server side
			// here only writes "ok" after ITS OWN Handshake() confirms
			// the client cert was accepted; a rejected client instead
			// sees the connection close with no response.
			go func(c net.Conn) {
				defer c.Close()
				tc, ok := c.(*tls.Conn)
				if !ok {
					return
				}
				if err := tc.Handshake(); err != nil {
					return
				}
				_, _ = tc.Write([]byte("ok"))
			}(conn)
		}
	}()

	addr := lis.Addr().String()

	dial := func(clientCerts []tls.Certificate) error {
		conn, err := tls.Dial("tcp", addr, &tls.Config{
			RootCAs:      mustPool(t, caCertPEM),
			Certificates: clientCerts,
			ServerName:   "127.0.0.1",
		})
		if err != nil {
			return err
		}
		defer conn.Close()
		buf := make([]byte, 2)
		_, err = io.ReadFull(conn, buf)
		return err
	}

	t.Run("valid client cert is accepted", func(t *testing.T) {
		clientCert, err := tls.X509KeyPair(clientCertPEM, clientKeyPEM)
		if err != nil {
			t.Fatalf("load client keypair: %v", err)
		}
		if err := dial([]tls.Certificate{clientCert}); err != nil {
			t.Fatalf("expected handshake to succeed with a trusted client cert, got: %v", err)
		}
	})

	t.Run("no client cert is rejected", func(t *testing.T) {
		if err := dial(nil); err == nil {
			t.Fatalf("expected handshake to fail with no client cert")
		}
	})

	t.Run("client cert from untrusted CA is rejected", func(t *testing.T) {
		untrustedCert, err := tls.X509KeyPair(untrustedClientCertPEM, untrustedClientKeyPEM)
		if err != nil {
			t.Fatalf("load untrusted client keypair: %v", err)
		}
		if err := dial([]tls.Certificate{untrustedCert}); err == nil {
			t.Fatalf("expected handshake to fail with an untrusted-CA client cert")
		}
	})
}

func TestServerOnlyTLS_NoClientCertRequired(t *testing.T) {
	dir := t.TempDir()
	caCertPEM, caKey, caCert := genCA(t, "unigate-test-ca")
	serverCertPEM, serverKeyPEM := genLeaf(t, caCert, caKey, "server", true)

	serverCertPath := writeFile(t, dir, "server.crt", serverCertPEM)
	serverKeyPath := writeFile(t, dir, "server.key", serverKeyPEM)

	serverTLSCfg, err := Build(config.TLSConfig{
		Enabled: true, CertFile: serverCertPath, KeyFile: serverKeyPath,
		RequireClientCert: false,
	})
	if err != nil {
		t.Fatalf("build server tls config: %v", err)
	}
	if serverTLSCfg.ClientAuth != tls.NoClientCert {
		t.Fatalf("expected NoClientCert when RequireClientCert=false, got %v", serverTLSCfg.ClientAuth)
	}

	lis, err := tls.Listen("tcp", "127.0.0.1:0", serverTLSCfg)
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	defer lis.Close()

	go func() {
		conn, err := lis.Accept()
		if err != nil {
			return
		}
		defer conn.Close()
		if tc, ok := conn.(*tls.Conn); ok {
			_ = tc.Handshake()
		}
	}()

	conn, err := tls.Dial("tcp", lis.Addr().String(), &tls.Config{
		RootCAs:    mustPool(t, caCertPEM),
		ServerName: "127.0.0.1",
	})
	if err != nil {
		t.Fatalf("expected server-only TLS to accept a client with no cert, got: %v", err)
	}
	defer conn.Close()
	if err := conn.Handshake(); err != nil {
		t.Fatalf("handshake: %v", err)
	}
}

func mustPool(t *testing.T, caCertPEM []byte) *x509.CertPool {
	t.Helper()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(caCertPEM) {
		t.Fatalf("failed to add CA cert to pool")
	}
	return pool
}
