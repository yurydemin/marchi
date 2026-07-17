package imapclient

import (
	"bufio"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"fmt"
	"math/big"
	"net"
	"strings"
	"testing"
	"time"
)

// fakeServerBehavior controls how the fake IMAP server responds, just
// enough to exercise Connect()'s stage classification and ListFolders()
// without needing a real IMAP server or Docker.
type fakeServerBehavior struct {
	loginOK       bool
	listResponses []string // raw untagged lines sent before the tagged LIST completion
}

// startFakePlaintextIMAPServer starts a minimal plaintext IMAP responder
// and returns its address.
func startFakePlaintextIMAPServer(t *testing.T, b fakeServerBehavior) string {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go acceptFakeConns(ln, b)
	return ln.Addr().String()
}

// startFakeTLSIMAPServer is the same, but behind a TLS listener using cert
// (which callers construct for a hostname that deliberately won't match
// what Connect() is asked to verify, to exercise the TLS-failure path).
func startFakeTLSIMAPServer(t *testing.T, cert tls.Certificate, b fakeServerBehavior) string {
	t.Helper()
	ln, err := tls.Listen("tcp", "127.0.0.1:0", &tls.Config{Certificates: []tls.Certificate{cert}})
	if err != nil {
		t.Fatalf("tls listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go acceptFakeConns(ln, b)
	return ln.Addr().String()
}

func acceptFakeConns(ln net.Listener, b fakeServerBehavior) {
	for {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		go serveFakeConn(conn, b)
	}
}

func serveFakeConn(conn net.Conn, b fakeServerBehavior) {
	defer conn.Close()
	w := bufio.NewWriter(conn)
	r := bufio.NewReader(conn)

	fmt.Fprint(w, "* OK IMAP4rev1 fake server ready\r\n")
	w.Flush()

	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		fields := strings.Fields(line)
		if len(fields) == 0 {
			continue
		}
		tag := fields[0]
		upper := strings.ToUpper(line)

		switch {
		case strings.Contains(upper, " LOGIN "):
			if b.loginOK {
				fmt.Fprintf(w, "%s OK LOGIN completed\r\n", tag)
			} else {
				fmt.Fprintf(w, "%s NO [AUTHENTICATIONFAILED] Authentication failed\r\n", tag)
			}
			w.Flush()
		case strings.Contains(upper, " LIST "):
			for _, resp := range b.listResponses {
				fmt.Fprintf(w, "%s\r\n", resp)
			}
			fmt.Fprintf(w, "%s OK LIST completed\r\n", tag)
			w.Flush()
		case strings.Contains(upper, "LOGOUT"):
			fmt.Fprintf(w, "* BYE logging out\r\n%s OK LOGOUT completed\r\n", tag)
			w.Flush()
			return
		default:
			fmt.Fprintf(w, "%s BAD unrecognized in fake server\r\n", tag)
			w.Flush()
		}
	}
}

// generateSelfSignedCert builds an in-memory self-signed cert for
// commonName, valid for an hour — enough to exercise real crypto/tls
// handshake and certificate validation in tests without any external
// dependency (Docker, a real CA, ...).
func generateSelfSignedCert(t *testing.T, commonName string) tls.Certificate {
	t.Helper()

	priv, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generating key: %v", err)
	}

	template := x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: commonName},
		DNSNames:              []string{commonName},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		KeyUsage:              x509.KeyUsageDigitalSignature | x509.KeyUsageCertSign,
		ExtKeyUsage:           []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth},
		BasicConstraintsValid: true,
		IsCA:                  true,
	}
	der, err := x509.CreateCertificate(rand.Reader, &template, &template, &priv.PublicKey, priv)
	if err != nil {
		t.Fatalf("creating certificate: %v", err)
	}

	certPEM := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalECPrivateKey(priv)
	if err != nil {
		t.Fatalf("marshaling key: %v", err)
	}
	keyPEM := pem.EncodeToMemory(&pem.Block{Type: "EC PRIVATE KEY", Bytes: keyDER})

	cert, err := tls.X509KeyPair(certPEM, keyPEM)
	if err != nil {
		t.Fatalf("X509KeyPair: %v", err)
	}
	return cert
}

func splitHostPort(t *testing.T, addr string) (string, int) {
	t.Helper()
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatalf("SplitHostPort(%q): %v", addr, err)
	}
	var port int
	if _, err := fmt.Sscanf(portStr, "%d", &port); err != nil {
		t.Fatalf("parsing port %q: %v", portStr, err)
	}
	return host, port
}
