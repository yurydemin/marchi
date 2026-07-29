package sync

import (
	"bufio"
	"fmt"
	"net"
	"regexp"
	"strconv"
	"strings"
	"testing"
)

// fakeMessage is one message a fakeFetchServer will hand back on UID FETCH.
type fakeMessage struct {
	uid   uint32
	flags string // IMAP flags, e.g. "\\Seen \\Answered" (space-separated, wire format)
	body  []byte
}

// fakeFetchServer is a minimal LOGIN/SELECT/UID FETCH/LOGOUT responder —
// just enough to exercise FetchNewMessages end to end without Docker or a
// real IMAP server.
type fakeFetchServer struct {
	uidValidity uint32
	uidNext     uint32
	messages    []fakeMessage
	// rejectLogin, if true, makes plain LOGIN always fail — used by
	// TestSyncAccount_OAuth2AccessToken_AuthenticatesViaXOAUTH2NotLogin
	// to prove SyncAccount's oauth2AccessToken parameter actually drives
	// AUTHENTICATE XOAUTH2 rather than silently falling back to LOGIN
	// with an empty password (the bug this test guards against).
	rejectLogin bool
}

var uidFetchRangeStart = regexp.MustCompile(`UID FETCH (\d+):`)

func startFakeFetchServer(t *testing.T, s fakeFetchServer) string {
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
			go serveFakeFetchConn(conn, s)
		}
	}()
	return ln.Addr().String()
}

func serveFakeFetchConn(conn net.Conn, s fakeFetchServer) {
	defer conn.Close()
	w := bufio.NewWriter(conn)
	r := bufio.NewReader(conn)

	fmt.Fprint(w, "* OK IMAP4rev1 fake fetch server ready\r\n")
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
		case strings.Contains(upper, "CAPABILITY"):
			fmt.Fprint(w, "* CAPABILITY IMAP4rev1 AUTH=XOAUTH2\r\n")
			fmt.Fprintf(w, "%s OK CAPABILITY completed\r\n", tag)
			w.Flush()

		case strings.Contains(upper, " LOGIN "):
			if s.rejectLogin {
				fmt.Fprintf(w, "%s NO [AUTHENTICATIONFAILED] basic auth disabled\r\n", tag)
			} else {
				fmt.Fprintf(w, "%s OK LOGIN completed\r\n", tag)
			}
			w.Flush()

		case strings.Contains(upper, " AUTHENTICATE XOAUTH2"):
			// Minimal AUTHENTICATE XOAUTH2 continuation exchange (always
			// succeeds) — internal/imapclient's own fake_server_test.go
			// covers the mechanics (including the failure path) at the
			// imapclient.Connect level already; this only needs to prove
			// SyncAccount's oauth2AccessToken parameter actually reaches
			// ConnectOptions.OAuth2AccessToken and drives XOAUTH2 instead
			// of LOGIN.
			fmt.Fprint(w, "+ \r\n")
			w.Flush()
			if _, err := r.ReadString('\n'); err != nil {
				return
			}
			fmt.Fprintf(w, "%s OK AUTHENTICATE completed\r\n", tag)
			w.Flush()

		case strings.Contains(upper, " LIST "):
			// Only used by TestSyncAccount_OAuth2AccessToken_AuthenticatesViaXOAUTH2NotLogin
			// (via SyncAccount -> SyncFolders -> imapclient.ListFolders) —
			// every other test in this file drives FetchNewMessages
			// directly and never issues LIST.
			fmt.Fprint(w, "* LIST (\\HasNoChildren) \"/\" \"INBOX\"\r\n")
			fmt.Fprintf(w, "%s OK LIST completed\r\n", tag)
			w.Flush()

		case strings.Contains(upper, " STATUS "):
			fmt.Fprintf(w, "* STATUS \"INBOX\" (UIDVALIDITY %d)\r\n", s.uidValidity)
			fmt.Fprintf(w, "%s OK STATUS completed\r\n", tag)
			w.Flush()

		case strings.Contains(upper, " SELECT ") || strings.Contains(upper, " EXAMINE "):
			// FetchNewMessages selects read-write since Phase 3 step 2
			// (archive_and_delete/archive_and_mark_read need STORE/EXPUNGE),
			// but this fake server treats both commands identically — none
			// of its tests exercise those two actions against it (see
			// fetch_rules_test.go's doc comment: that's covered live
			// against real Dovecot instead).
			fmt.Fprintf(w, "* %d EXISTS\r\n", len(s.messages))
			fmt.Fprint(w, "* 0 RECENT\r\n")
			fmt.Fprint(w, "* FLAGS (\\Answered \\Flagged \\Deleted \\Seen \\Draft)\r\n")
			fmt.Fprintf(w, "* OK [UIDVALIDITY %d] UIDs valid\r\n", s.uidValidity)
			fmt.Fprintf(w, "* OK [UIDNEXT %d] Predicted next UID\r\n", s.uidNext)
			fmt.Fprintf(w, "%s OK [READ-ONLY] SELECT completed\r\n", tag)
			w.Flush()

		case strings.Contains(upper, "UID FETCH"):
			start := uint32(0)
			if m := uidFetchRangeStart.FindStringSubmatch(line); m != nil {
				n, _ := strconv.ParseUint(m[1], 10, 32)
				start = uint32(n)
			}
			seqNum := 0
			for _, msg := range s.messages {
				if msg.uid < start {
					continue
				}
				seqNum++
				fmt.Fprintf(w, "* %d FETCH (UID %d FLAGS (%s) BODY[] {%d}\r\n", seqNum, msg.uid, msg.flags, len(msg.body))
				w.Write(msg.body)
				fmt.Fprint(w, ")\r\n")
			}
			fmt.Fprintf(w, "%s OK UID FETCH completed\r\n", tag)
			w.Flush()

		case strings.Contains(upper, "LOGOUT"):
			fmt.Fprintf(w, "* BYE logging out\r\n%s OK LOGOUT completed\r\n", tag)
			w.Flush()
			return

		default:
			fmt.Fprintf(w, "%s BAD unrecognized in fake fetch server\r\n", tag)
			w.Flush()
		}
	}
}
