package restore

import (
	"bufio"
	"encoding/base64"
	"net"
	"strings"
	"sync"
	"testing"
)

// fakeSMTPServer is a minimal EHLO/AUTH PLAIN|XOAUTH2/MAIL/RCPT/DATA
// responder — just enough to exercise trySMTP's envelope + raw-body
// transmission without a real SMTP server, mirroring internal/sync's
// fakeFetchServer for IMAP FETCH.
type fakeSMTPServer struct {
	mu       sync.Mutex
	mailFrom string
	rcptTo   string
	data     []byte

	// xoauth2OK controls AUTH XOAUTH2's outcome: nil means "accept
	// anything" (the two AUTH-PLAIN-only tests never send it), non-nil
	// selects success or Google's real two-step failure protocol
	// (334 error challenge, empty reply, then 535).
	xoauth2OK *bool
}

func startFakeSMTPServer(t *testing.T) (addr string, srv *fakeSMTPServer) {
	t.Helper()
	return startFakeSMTPServerWith(t, &fakeSMTPServer{})
}

// startFakeSMTPServerXOAUTH2 starts a fake server whose AUTH XOAUTH2
// handling succeeds or fails per ok.
func startFakeSMTPServerXOAUTH2(t *testing.T, ok bool) (addr string, srv *fakeSMTPServer) {
	t.Helper()
	return startFakeSMTPServerWith(t, &fakeSMTPServer{xoauth2OK: &ok})
}

func startFakeSMTPServerWith(t *testing.T, srv *fakeSMTPServer) (addr string, _ *fakeSMTPServer) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go srv.serve(conn)
		}
	}()
	return ln.Addr().String(), srv
}

func (s *fakeSMTPServer) serve(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	w := conn

	write := func(line string) { w.Write([]byte(line + "\r\n")) }

	write("220 fake-smtp ready")
	for {
		line, err := r.ReadString('\n')
		if err != nil {
			return
		}
		line = strings.TrimRight(line, "\r\n")
		upper := strings.ToUpper(line)

		switch {
		case strings.HasPrefix(upper, "EHLO"):
			write("250-fake-smtp")
			write("250-AUTH PLAIN LOGIN XOAUTH2")
			write("250 8BITMIME")
		case strings.HasPrefix(upper, "AUTH XOAUTH2"):
			parts := strings.SplitN(line, " ", 3)
			if len(parts) == 3 {
				_, _ = base64.StdEncoding.DecodeString(parts[2])
			}
			if s.xoauth2OK == nil || *s.xoauth2OK {
				write("235 2.7.0 Authentication successful")
				continue
			}
			errJSON := base64.StdEncoding.EncodeToString([]byte(`{"status":"400","schemes":"bearer","scope":"https://mail.google.com/"}`))
			write("334 " + errJSON)
			if _, err := r.ReadString('\n'); err != nil {
				return
			}
			write("535 5.7.8 Authentication failed")
		case strings.HasPrefix(upper, "AUTH PLAIN"):
			// Accept either "AUTH PLAIN <b64>" in one line or the
			// two-step "AUTH PLAIN" then a continuation line.
			parts := strings.SplitN(line, " ", 3)
			if len(parts) == 3 {
				write("235 2.7.0 Authentication successful")
				continue
			}
			write("334 ")
			contLine, err := r.ReadString('\n')
			if err != nil {
				return
			}
			_, _ = base64.StdEncoding.DecodeString(strings.TrimRight(contLine, "\r\n"))
			write("235 2.7.0 Authentication successful")
		case strings.HasPrefix(upper, "MAIL FROM:"):
			s.mu.Lock()
			s.mailFrom = extractAddr(line)
			s.mu.Unlock()
			write("250 2.1.0 OK")
		case strings.HasPrefix(upper, "RCPT TO:"):
			s.mu.Lock()
			s.rcptTo = extractAddr(line)
			s.mu.Unlock()
			write("250 2.1.5 OK")
		case strings.HasPrefix(upper, "DATA"):
			write("354 Start mail input; end with <CRLF>.<CRLF>")
			var buf []byte
			for {
				dataLine, err := r.ReadString('\n')
				if err != nil {
					return
				}
				if dataLine == ".\r\n" || dataLine == ".\n" {
					break
				}
				buf = append(buf, []byte(dataLine)...)
			}
			s.mu.Lock()
			s.data = buf
			s.mu.Unlock()
			write("250 2.0.0 OK: queued")
		case strings.HasPrefix(upper, "QUIT"):
			write("221 2.0.0 Bye")
			return
		default:
			write("250 OK")
		}
	}
}

// extractAddr pulls the address out of a MAIL FROM:/RCPT TO: line,
// handling both the bracketed form ("MAIL FROM:<addr>") and the
// unbracketed form some clients send, plus any trailing SMTP extension
// parameters (e.g. "BODY=8BITMIME").
func extractAddr(line string) string {
	_, rest, ok := strings.Cut(line, ":")
	if !ok {
		return line
	}
	rest = strings.TrimSpace(rest)
	if strings.HasPrefix(rest, "<") {
		if end := strings.Index(rest, ">"); end > 0 {
			return rest[1:end]
		}
	}
	if sp := strings.IndexByte(rest, ' '); sp >= 0 {
		rest = rest[:sp]
	}
	return rest
}

func (s *fakeSMTPServer) received() (mailFrom, rcptTo string, data []byte) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mailFrom, s.rcptTo, s.data
}
