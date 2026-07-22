package httpapi

import (
	"context"
	"database/sql"
	"errors"
	"strconv"
	"time"

	"github.com/gofiber/fiber/v2"
	"github.com/gofiber/fiber/v2/middleware/adaptor"
	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/collectors"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

// httpRequestDuration is shared between registerMetrics (which registers
// it into the scrape registry) and httpMetricsMiddleware (which observes
// into it on every request). route is the matched route pattern
// (c.Route().Path, e.g. "/api/v1/emails/:id") rather than the raw URL —
// using the raw URL would give every distinct email/account/rule ID its
// own label series, an unbounded-cardinality metric that gets worse the
// longer the archive runs.
var httpRequestDuration = prometheus.NewHistogramVec(
	prometheus.HistogramOpts{
		Namespace: "marchi",
		Name:      "http_request_duration_seconds",
		Help:      "HTTP request latency in seconds, by method/route/status.",
		Buckets:   prometheus.DefBuckets,
	},
	[]string{"method", "route", "status"},
)

// httpMetricsMiddleware times every request end-to-end (registered right
// after recover.New(), before anything else, so the duration includes
// every other middleware in the chain). By the time c.Next() returns,
// Fiber's error handler has already turned any handler error into its
// final response, so c.Response().StatusCode() is the real status the
// client saw.
func httpMetricsMiddleware(c *fiber.Ctx) error {
	start := time.Now()
	err := c.Next()
	httpRequestDuration.WithLabelValues(
		c.Method(), c.Route().Path, strconv.Itoa(c.Response().StatusCode()),
	).Observe(time.Since(start).Seconds())
	return err
}

// registerMetrics wires GET /metrics (Phase 4 step 2), a Prometheus
// scrape endpoint. It's registered as its own path rather than under
// /api/v1, so newLockGate's prefix check (unlock.go) never applies to
// it — a scraper has no browser session to unlock with, and shouldn't
// need one just to see that the vault is locked. Process-level metrics
// (Go runtime, HTTP latency) are always present; archiveCollector's
// business gauges just report marchi_unlocked=0 and nothing else while
// locked, which Prometheus surfaces as an absent series, not a zero.
func registerMetrics(app *fiber.App, vault *vaultState) {
	reg := prometheus.NewRegistry()
	reg.MustRegister(collectors.NewGoCollector())
	reg.MustRegister(collectors.NewProcessCollector(collectors.ProcessCollectorOpts{}))
	reg.MustRegister(httpRequestDuration)
	reg.MustRegister(newArchiveCollector(vault))

	app.Get("/metrics", adaptor.HTTPHandler(promhttp.HandlerFor(reg, promhttp.HandlerOpts{})))
}

// archiveCollector reads every business metric fresh from the repos at
// scrape time rather than needing a hook call scattered through
// sync/s3store/restore's business logic. This works because sync_logs,
// restore_logs, and s3_upload_queue rows are never deleted — a status's
// row count only grows, so COUNT(*) GROUP BY status behaves exactly like
// a monotonic counter would, just computed on demand instead of
// incremented in place. The one metric that can't be derived this way is
// HTTP latency (httpRequestDuration), which genuinely needs a live
// per-request hook.
type archiveCollector struct {
	vault *vaultState

	unlocked        *prometheus.Desc
	emailsTotal     *prometheus.Desc
	storageBytes    *prometheus.Desc
	accountsTotal   *prometheus.Desc
	syncRuns        *prometheus.Desc
	s3QueueDepth    *prometheus.Desc
	restoreAttempts *prometheus.Desc
	s3Configured    *prometheus.Desc
	s3Enabled       *prometheus.Desc
}

func newArchiveCollector(vault *vaultState) *archiveCollector {
	return &archiveCollector{
		vault:           vault,
		unlocked:        prometheus.NewDesc("marchi_unlocked", "1 if the vault is currently unlocked, 0 otherwise.", nil, nil),
		emailsTotal:     prometheus.NewDesc("marchi_emails_total", "Total archived emails.", nil, nil),
		storageBytes:    prometheus.NewDesc("marchi_storage_bytes", "Archive storage volume by location.", []string{"location"}, nil),
		accountsTotal:   prometheus.NewDesc("marchi_accounts_total", "Configured IMAP accounts by status.", []string{"status"}, nil),
		syncRuns:        prometheus.NewDesc("marchi_sync_runs_total", "Sync runs recorded, by outcome.", []string{"status"}, nil),
		s3QueueDepth:    prometheus.NewDesc("marchi_s3_upload_queue_depth", "S3 mirror upload queue depth, by status.", []string{"status"}, nil),
		restoreAttempts: prometheus.NewDesc("marchi_restore_attempts_total", "Restore attempts recorded, by outcome.", []string{"status"}, nil),
		s3Configured:    prometheus.NewDesc("marchi_s3_configured", "1 if S3 mirroring has ever been configured.", nil, nil),
		s3Enabled:       prometheus.NewDesc("marchi_s3_enabled", "1 if S3 mirroring is currently enabled.", nil, nil),
	}
}

func (c *archiveCollector) Describe(ch chan<- *prometheus.Desc) {
	ch <- c.unlocked
	ch <- c.emailsTotal
	ch <- c.storageBytes
	ch <- c.accountsTotal
	ch <- c.syncRuns
	ch <- c.s3QueueDepth
	ch <- c.restoreAttempts
	ch <- c.s3Configured
	ch <- c.s3Enabled
}

func (c *archiveCollector) Collect(ch chan<- prometheus.Metric) {
	b := c.vault.currentBackend()
	if b == nil {
		ch <- prometheus.MustNewConstMetric(c.unlocked, prometheus.GaugeValue, 0)
		return
	}
	ch <- prometheus.MustNewConstMetric(c.unlocked, prometheus.GaugeValue, 1)

	ctx := context.Background()

	if stats, err := b.emailsRepo.Stats(ctx); err == nil {
		ch <- prometheus.MustNewConstMetric(c.emailsTotal, prometheus.GaugeValue, float64(stats.Total))
		ch <- prometheus.MustNewConstMetric(c.storageBytes, prometheus.GaugeValue, float64(stats.LocalBytes), "local")
		ch <- prometheus.MustNewConstMetric(c.storageBytes, prometheus.GaugeValue, float64(stats.S3Bytes), "s3")
	}

	if accounts, err := b.accountsRepo.List(ctx); err == nil {
		active, paused := 0, 0
		for _, a := range accounts {
			if a.IsActive {
				active++
			} else {
				paused++
			}
		}
		ch <- prometheus.MustNewConstMetric(c.accountsTotal, prometheus.GaugeValue, float64(active), "active")
		ch <- prometheus.MustNewConstMetric(c.accountsTotal, prometheus.GaugeValue, float64(paused), "paused")
	}

	if counts, err := b.syncLogsRepo.CountByStatus(ctx); err == nil {
		for status, n := range counts {
			ch <- prometheus.MustNewConstMetric(c.syncRuns, prometheus.GaugeValue, float64(n), status)
		}
	}

	if counts, err := b.s3UploadQueueRepo.CountByStatus(ctx); err == nil {
		for status, n := range counts {
			ch <- prometheus.MustNewConstMetric(c.s3QueueDepth, prometheus.GaugeValue, float64(n), status)
		}
	}

	if counts, err := b.restoreLogsRepo.CountByStatus(ctx); err == nil {
		for status, n := range counts {
			ch <- prometheus.MustNewConstMetric(c.restoreAttempts, prometheus.GaugeValue, float64(n), status)
		}
	}

	switch s3cfg, err := b.s3ConfigManager.Get(ctx); {
	case err == nil:
		ch <- prometheus.MustNewConstMetric(c.s3Configured, prometheus.GaugeValue, 1)
		enabled := 0.0
		if s3cfg.Enabled {
			enabled = 1
		}
		ch <- prometheus.MustNewConstMetric(c.s3Enabled, prometheus.GaugeValue, enabled)
	case errors.Is(err, sql.ErrNoRows):
		ch <- prometheus.MustNewConstMetric(c.s3Configured, prometheus.GaugeValue, 0)
		ch <- prometheus.MustNewConstMetric(c.s3Enabled, prometheus.GaugeValue, 0)
	}
}
