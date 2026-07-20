package logging

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"sync"
	"time"
)

// dailyRotatingWriter implements NFR-RL-04: one file per calendar day named
// {dir}/marchi-{YYYY-MM-DD}.log, capped at maxBytes (overflow within the
// same day spills into marchi-{date}.2.log, .3.log, ...), with files older
// than maxAgeDays swept on each day rollover.
type dailyRotatingWriter struct {
	mu         sync.Mutex
	dir        string
	prefix     string
	maxBytes   int64
	maxAgeDays int
	now        func() time.Time

	curDate string
	curPart int
	curFile *os.File
	curSize int64
}

var filenamePattern = regexp.MustCompile(`^([a-zA-Z0-9_-]+)-(\d{4}-\d{2}-\d{2})(?:\.\d+)?\.log$`)

func newDailyRotatingWriter(dir, prefix string, maxBytes int64, maxAgeDays int, now func() time.Time) (*dailyRotatingWriter, error) {
	if now == nil {
		now = time.Now
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return nil, fmt.Errorf("creating log dir %s: %w", dir, err)
	}
	w := &dailyRotatingWriter{
		dir:        dir,
		prefix:     prefix,
		maxBytes:   maxBytes,
		maxAgeDays: maxAgeDays,
		now:        now,
	}
	if err := w.rotate(now().Format("2006-01-02")); err != nil {
		return nil, err
	}
	return w, nil
}

func (w *dailyRotatingWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	today := w.now().Format("2006-01-02")
	if today != w.curDate {
		if err := w.rotate(today); err != nil {
			return 0, err
		}
	} else if w.maxBytes > 0 && w.curSize+int64(len(p)) > w.maxBytes {
		if err := w.rotateSamedayOverflow(); err != nil {
			return 0, err
		}
	}

	n, err := w.curFile.Write(p)
	w.curSize += int64(n)
	return n, err
}

// rotate opens the file for a new day, resetting the overflow part counter,
// and sweeps files older than maxAgeDays.
func (w *dailyRotatingWriter) rotate(date string) error {
	if w.curFile != nil {
		_ = w.curFile.Close()
	}
	w.curDate = date
	w.curPart = 1
	if err := w.openCurrent(); err != nil {
		return err
	}
	return w.sweepOld(date)
}

// rotateSamedayOverflow starts a new part file within the same day because
// the current one hit maxBytes.
func (w *dailyRotatingWriter) rotateSamedayOverflow() error {
	if w.curFile != nil {
		_ = w.curFile.Close()
	}
	w.curPart++
	return w.openCurrent()
}

func (w *dailyRotatingWriter) filename() string {
	if w.curPart <= 1 {
		return fmt.Sprintf("%s-%s.log", w.prefix, w.curDate)
	}
	return fmt.Sprintf("%s-%s.%d.log", w.prefix, w.curDate, w.curPart)
}

func (w *dailyRotatingWriter) openCurrent() error {
	path := filepath.Join(w.dir, w.filename())
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return fmt.Errorf("opening log file %s: %w", path, err)
	}
	info, err := f.Stat()
	if err != nil {
		_ = f.Close()
		return fmt.Errorf("stat log file %s: %w", path, err)
	}
	w.curFile = f
	w.curSize = info.Size()
	return nil
}

// sweepOld deletes rotated log files older than maxAgeDays, relative to today.
func (w *dailyRotatingWriter) sweepOld(today string) error {
	if w.maxAgeDays <= 0 {
		return nil
	}
	cutoff, err := time.Parse("2006-01-02", today)
	if err != nil {
		return nil
	}
	cutoff = cutoff.AddDate(0, 0, -w.maxAgeDays)

	entries, err := os.ReadDir(w.dir)
	if err != nil {
		return fmt.Errorf("reading log dir %s: %w", w.dir, err)
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		m := filenamePattern.FindStringSubmatch(e.Name())
		if m == nil || m[1] != w.prefix {
			continue
		}
		fileDate, err := time.Parse("2006-01-02", m[2])
		if err != nil {
			continue
		}
		if fileDate.Before(cutoff) {
			_ = os.Remove(filepath.Join(w.dir, e.Name()))
		}
	}
	return nil
}

func (w *dailyRotatingWriter) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.curFile == nil {
		return nil
	}
	err := w.curFile.Close()
	w.curFile = nil
	return err
}
