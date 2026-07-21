package httpapi

import (
	"context"
	"fmt"
	"os"

	"github.com/yurydemin/marchi/internal/domain"
)

// loadEmailContent returns e's raw .eml bytes regardless of where it
// currently lives — read straight off disk for storage_location=local,
// or lazily downloaded and decrypted from S3 (FR-RS-03) otherwise. Every
// reader of an archived email's content (preview, .eml download,
// attachment download, the Archive UI viewer) goes through this so none
// of them silently treat an S3-resident email as unavailable, the way
// they did before Phase 3 step 19 wired S3 into these paths.
func loadEmailContent(ctx context.Context, b *backend, e *domain.Email) ([]byte, error) {
	if e.StorageLocation == "local" {
		if e.LocalPath == "" {
			return nil, fmt.Errorf("email has no local path recorded")
		}
		return os.ReadFile(e.LocalPath)
	}
	if b.lazyLoader == nil {
		return nil, fmt.Errorf("email is stored in S3 but S3 is not configured")
	}
	return b.lazyLoader.Load(ctx, e.S3Key)
}
