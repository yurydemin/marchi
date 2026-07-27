package notify

import (
	"bufio"
	"net"
	"strings"
	"sync"
	"testing"
)

// fakeSMTPServer is a minimal EHLO/AUTH PLAIN/MAIL/RCPT/DATA responder —
// just enough to exercise EmailNotifier.Notify without a real SMTP
// server, mirroring internal/restore's own fake SMTP test server (a
// separate, smaller copy rather than a shared export: this one doesn't
// need XOAUTH2, and internal/notify/internal/restore aren't meant to
// depend on each other for test-only code).
type fakeSMTPServer struct {
	mu       sync.Mutex
	mailFrom string
	rcptTo   []string
	data     []byte
	authSeen bool
}

func startFakeSMTPServer(t *testing.T) (host string, port int, srv *fakeSMTPServer) {
	t.Helper()
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	t.Cleanup(func() { ln.Close() })

	srv = &fakeSMTPServer{}
	go func() {
		for {
			conn, err := ln.Accept()
			if err != nil {
				return
			}
			go srv.serve(conn)
		}
	}()

	addr := ln.Addr().(*net.TCPAddr)
	return addr.IP.String(), addr.Port, srv
}

func (s *fakeSMTPServer) serve(conn net.Conn) {
	defer conn.Close()
	r := bufio.NewReader(conn)
	write := func(line string) { conn.Write([]byte(line + "\r\n")) }

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
			write("250-AUTH PLAIN LOGIN")
			write("250 8BITMIME")
		case strings.HasPrefix(upper, "AUTH PLAIN"):
			s.mu.Lock()
			s.authSeen = true
			s.mu.Unlock()
			parts := strings.SplitN(line, " ", 3)
			if len(parts) == 3 {
				write("235 2.7.0 Authentication successful")
				continue
			}
			write("334 ")
			if _, err := r.ReadString('\n'); err != nil {
				return
			}
			write("235 2.7.0 Authentication successful")
		case strings.HasPrefix(upper, "MAIL FROM:"):
			s.mu.Lock()
			s.mailFrom = extractAddr(line)
			s.mu.Unlock()
			write("250 2.1.0 OK")
		case strings.HasPrefix(upper, "RCPT TO:"):
			addr := extractAddr(line)
			s.mu.Lock()
			s.rcptTo = append(s.rcptTo, addr)
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

func (s *fakeSMTPServer) received() (mailFrom string, rcptTo []string, data []byte, authSeen bool) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.mailFrom, s.rcptTo, s.data, s.authSeen
}
