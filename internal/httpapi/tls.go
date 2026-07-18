package httpapi

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"os"
	"path/filepath"
	"time"

	"github.com/yurydemin/marchi/internal/config"
)

// certValidity is deliberately long (10 years): this is a self-signed,
// localhost-only convenience certificate (NFR-SC-04), not a publicly
// trusted one. Browsers will always show a trust warning for it regardless
// of validity period — an operator who wants that warning gone supplies
// their own cert via tls.cert_file/key_file instead.
const certValidity = 10 * 365 * 24 * time.Hour

// EnsureTLSCert resolves the certificate/key pair the HTTP server should
// listen with. An operator-supplied pair (tls.cert_file/key_file) always
// wins; if either is set but unreadable, that's a configuration error —
// silently falling back to a self-signed pair in its place would be
// actively misleading. Otherwise, with tls.auto_cert enabled, a self-signed
// pair is generated under {data_dir}/tls/ on first run and reused on every
// subsequent call — regenerating it on every startup would invalidate any
// browser exception the operator already granted it.
func EnsureTLSCert(cfg *config.Config) (certFile, keyFile string, err error) {
	if cfg.HTTP.TLS.CertFile != "" || cfg.HTTP.TLS.KeyFile != "" {
		if cfg.HTTP.TLS.CertFile == "" || cfg.HTTP.TLS.KeyFile == "" {
			return "", "", fmt.Errorf("httpapi: tls.cert_file and tls.key_file must both be set, or both left empty")
		}
		if _, err := os.Stat(cfg.HTTP.TLS.CertFile); err != nil {
			return "", "", fmt.Errorf("httpapi: tls.cert_file: %w", err)
		}
		if _, err := os.Stat(cfg.HTTP.TLS.KeyFile); err != nil {
			return "", "", fmt.Errorf("httpapi: tls.key_file: %w", err)
		}
		return cfg.HTTP.TLS.CertFile, cfg.HTTP.TLS.KeyFile, nil
	}

	if !cfg.HTTP.TLS.AutoCert {
		return "", "", fmt.Errorf("httpapi: tls.enabled but neither cert_file/key_file nor auto_cert is set")
	}

	certPath := filepath.Join(cfg.TLSDir(), "cert.pem")
	keyPath := filepath.Join(cfg.TLSDir(), "key.pem")

	if _, err := os.Stat(certPath); err == nil {
		if _, err := os.Stat(keyPath); err == nil {
			return certPath, keyPath, nil
		}
	}

	if err := generateSelfSigned(certPath, keyPath); err != nil {
		return "", "", err
	}
	return certPath, keyPath, nil
}

func generateSelfSigned(certPath, keyPath string) error {
	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		return fmt.Errorf("httpapi: generating key: %w", err)
	}

	serial, err := rand.Int(rand.Reader, new(big.Int).Lsh(big.NewInt(1), 128))
	if err != nil {
		return fmt.Errorf("httpapi: generating serial number: %w", err)
	}

	template := x509.Certificate{
		SerialNumber:          serial,
		Subject:               pkix.Name{CommonName: "MailVault (self-signed)"},
		NotBefore:             time.Now(),
		NotAfter:              time.Now().Add(certValidity),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageKeyEncipherment,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		DNSNames:              []string{"localhost"},
		IPAddresses:           []net.IP{net.IPv4(127, 0, 0, 1), net.IPv6loopback},
	}

	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		return fmt.Errorf("httpapi: creating certificate: %w", err)
	}
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		return fmt.Errorf("httpapi: marshaling key: %w", err)
	}

	if err := os.MkdirAll(filepath.Dir(certPath), 0o700); err != nil {
		return fmt.Errorf("httpapi: creating tls dir: %w", err)
	}
	if err := writePEMFile(certPath, "CERTIFICATE", der, 0o644); err != nil {
		return err
	}
	if err := writePEMFile(keyPath, "EC PRIVATE KEY", keyDER, 0o600); err != nil {
		return err
	}
	return nil
}

func writePEMFile(path, blockType string, der []byte, perm os.FileMode) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_TRUNC, perm)
	if err != nil {
		return fmt.Errorf("httpapi: writing %s: %w", path, err)
	}
	defer f.Close()
	return pem.Encode(f, &pem.Block{Type: blockType, Bytes: der})
}
