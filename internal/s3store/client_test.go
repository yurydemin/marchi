package s3store

import (
	"bytes"
	"context"
	"io"
	"testing"
	"time"

	"github.com/yurydemin/marchi/internal/testutil/minio"
)

func TestEmailKey(t *testing.T) {
	date := time.Date(2026, time.March, 5, 0, 0, 0, 0, time.UTC)
	got := EmailKey(7, date, "abcd1234")
	want := "marchi/v1/accounts/7/emails/2026/03/05/ab/abcd1234.eml"
	if got != want {
		t.Errorf("EmailKey() = %q, want %q", got, want)
	}
}

func TestAttachmentKey(t *testing.T) {
	got := AttachmentKey(7, "abcd1234", "invoice.pdf")
	want := "marchi/v1/accounts/7/attachments/abcd1234/invoice.pdf"
	if got != want {
		t.Errorf("AttachmentKey() = %q, want %q", got, want)
	}
}

func newTestClient(t *testing.T) *Client {
	t.Helper()
	srv := minio.Start(t)

	c, err := NewClient(Options{
		Endpoint:        srv.Endpoint,
		Region:          "us-east-1",
		Bucket:          "marchi-test",
		AccessKeyID:     srv.AccessKeyID,
		SecretAccessKey: srv.SecretKey,
		PathStyle:       true,
	})
	if err != nil {
		t.Fatalf("NewClient: %v", err)
	}
	if err := c.EnsureBucket(context.Background()); err != nil {
		t.Fatalf("EnsureBucket: %v", err)
	}
	return c
}

func TestClient_PutGetDelete_RoundTrip(t *testing.T) {
	c := newTestClient(t)
	ctx := context.Background()

	key := EmailKey(1, time.Date(2026, time.July, 19, 0, 0, 0, 0, time.UTC), "deadbeef")
	content := []byte("From: a@example.com\r\nSubject: test\r\n\r\nbody")
	meta := map[string]string{"sha256": "deadbeef"}

	etag, err := c.Put(ctx, key, bytes.NewReader(content), meta)
	if err != nil {
		t.Fatalf("Put: %v", err)
	}
	if etag == "" {
		t.Error("Put returned empty ETag")
	}

	body, gotMeta, err := c.Get(ctx, key)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	got, err := io.ReadAll(body)
	body.Close()
	if err != nil {
		t.Fatalf("reading body: %v", err)
	}
	if !bytes.Equal(got, content) {
		t.Errorf("Get body = %q, want %q", got, content)
	}
	if gotMeta["sha256"] != "deadbeef" {
		t.Errorf("Get metadata[sha256] = %q, want %q", gotMeta["sha256"], "deadbeef")
	}

	if err := c.Delete(ctx, key); err != nil {
		t.Fatalf("Delete: %v", err)
	}

	if _, _, err := c.Get(ctx, key); err == nil {
		t.Error("Get after Delete succeeded, want an error")
	}
}

func TestClient_Delete_AbsentKeyIsNotError(t *testing.T) {
	c := newTestClient(t)
	if err := c.Delete(context.Background(), "marchi/v1/accounts/1/emails/never/existed.eml"); err != nil {
		t.Errorf("Delete of absent key = %v, want nil", err)
	}
}

func TestClient_EnsureBucket_IdempotentOnSecondCall(t *testing.T) {
	c := newTestClient(t)
	if err := c.EnsureBucket(context.Background()); err != nil {
		t.Errorf("second EnsureBucket = %v, want nil", err)
	}
}
