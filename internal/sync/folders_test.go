package sync

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/yurydemin/marchi/internal/db"
	"github.com/yurydemin/marchi/internal/db/repo"
	"github.com/yurydemin/marchi/internal/db/writer"
	"github.com/yurydemin/marchi/internal/domain"
	"github.com/yurydemin/marchi/internal/imapclient"
)

// startFakeIMAPServer is a minimal plaintext LOGIN/LIST/STATUS/LOGOUT
// responder — just enough to exercise SyncFolders without Docker or a real
// IMAP server (which internal/imapclient's own tests already cover for the
// connect/error-classification concerns; this one is scoped to SyncFolders'
// own orchestration: skip \Noselect, plumb UIDVALIDITY through per folder).
func startFakeIMAPServer(t *testing.T, uidValidity map[string]uint32) string {
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
			go serveFakeConn(conn, uidValidity)
		}
	}()
	return ln.Addr().String()
}

func serveFakeConn(conn net.Conn, uidValidity map[string]uint32) {
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
			fmt.Fprintf(w, "%s OK LOGIN completed\r\n", tag)
			w.Flush()
		case strings.Contains(upper, " LIST "):
			fmt.Fprint(w, `* LIST (\HasNoChildren) "/" "INBOX"`+"\r\n")
			fmt.Fprint(w, `* LIST (\HasNoChildren) "/" "Archive"`+"\r\n")
			fmt.Fprint(w, `* LIST (\Noselect \HasChildren) "/" "[Gmail]"`+"\r\n")
			fmt.Fprintf(w, "%s OK LIST completed\r\n", tag)
			w.Flush()
		case strings.Contains(upper, " STATUS "):
			// fields: <tag> STATUS <mailbox> (UIDVALIDITY)
			mailbox := strings.Trim(fields[2], `"`)
			uv := uidValidity[mailbox]
			fmt.Fprintf(w, "* STATUS %s (UIDVALIDITY %d)\r\n", mailbox, uv)
			fmt.Fprintf(w, "%s OK STATUS completed\r\n", tag)
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

func TestSyncFolders(t *testing.T) {
	addr := startFakeIMAPServer(t, map[string]uint32{
		"INBOX":   1001,
		"Archive": 2002,
	})
	host, portStr, err := net.SplitHostPort(addr)
	if err != nil {
		t.Fatal(err)
	}
	port, err := strconv.Atoi(portStr)
	if err != nil {
		t.Fatal(err)
	}

	c, err := imapclient.Connect(context.Background(), imapclient.ConnectOptions{
		Host: host, Port: port, TLS: domain.IMAPTLSNone,
		Username: "user", Password: "pass", DialTimeout: 5 * time.Second,
	})
	if err != nil {
		t.Fatalf("Connect: %v", err)
	}
	defer c.Logout()

	sqlDB, err := db.Open(filepath.Join(t.TempDir(), "mailvault.db"))
	if err != nil {
		t.Fatalf("db.Open: %v", err)
	}
	defer sqlDB.Close()
	w := writer.New(sqlDB)
	defer w.Close()

	accountsRepo := repo.NewAccountsRepo(sqlDB, w)
	accountID, err := accountsRepo.Create(context.Background(), &domain.Account{
		Email: "user@example.com", IMAPHost: host, IMAPPort: port, IsActive: true,
	})
	if err != nil {
		t.Fatalf("creating account fixture: %v", err)
	}
	foldersRepo := repo.NewFoldersRepo(sqlDB, w)

	folders, err := SyncFolders(context.Background(), c, accountID, foldersRepo)
	if err != nil {
		t.Fatalf("SyncFolders: %v", err)
	}

	if len(folders) != 2 {
		t.Fatalf("got %d folders, want 2 (the \\Noselect [Gmail] node must be skipped): %+v", len(folders), folders)
	}

	byName := map[string]*domain.Folder{}
	for _, f := range folders {
		byName[f.FolderName] = f
	}
	if f, ok := byName["INBOX"]; !ok || f.UIDValidity != 1001 {
		t.Errorf("INBOX = %+v, want UIDValidity 1001", f)
	}
	if f, ok := byName["Archive"]; !ok || f.UIDValidity != 2002 {
		t.Errorf("Archive = %+v, want UIDValidity 2002", f)
	}

	// Persisted, not just returned in memory.
	stored, err := foldersRepo.ListByAccount(context.Background(), accountID)
	if err != nil {
		t.Fatalf("ListByAccount: %v", err)
	}
	if len(stored) != 2 {
		t.Errorf("got %d stored folders, want 2", len(stored))
	}
}
