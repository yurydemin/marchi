package httpapi

import (
	"context"
	"testing"

	"github.com/yurydemin/marchi/internal/s3config"
)

func TestComputeStats_S3NeverConfigured_ReportsUnconfigured(t *testing.T) {
	b := newTestBackend(t)
	resp, err := computeStats(context.Background(), b)
	if err != nil {
		t.Fatalf("computeStats: %v", err)
	}
	if resp.S3Configured {
		t.Error("S3Configured = true, want false before any Save")
	}
	if resp.S3Enabled {
		t.Error("S3Enabled = true, want false before any Save")
	}
}

func TestComputeStats_S3Configured_ReflectsEnabledAndQueueCounts(t *testing.T) {
	b := newTestBackend(t)
	ctx := context.Background()

	if _, err := b.s3ConfigManager.Save(ctx, s3config.SaveParams{
		Enabled: true, Bucket: "mailvault", Region: "us-east-1",
		AccessKey: "AKIAEXAMPLE", SecretKey: "supersecret",
	}); err != nil {
		t.Fatalf("Save: %v", err)
	}

	resp, err := computeStats(ctx, b)
	if err != nil {
		t.Fatalf("computeStats: %v", err)
	}
	if !resp.S3Configured {
		t.Error("S3Configured = false, want true after Save")
	}
	if !resp.S3Enabled {
		t.Error("S3Enabled = false, want true (Save was called with Enabled: true)")
	}
	if resp.S3QueuePending != 0 || resp.S3QueueUploading != 0 || resp.S3QueueFailed != 0 {
		t.Errorf("queue counts = pending:%d uploading:%d failed:%d, want all 0 (nothing enqueued yet)",
			resp.S3QueuePending, resp.S3QueueUploading, resp.S3QueueFailed)
	}
}
