package rules

import (
	"context"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
	"go.uber.org/zap"

	"github.com/yurydemin/marchi/internal/db/repo"
)

// debounceInterval collapses a burst of fsnotify events from a single
// logical save (many editors/tools emit several Write/Create/Chmod events
// per save) into one reload.
const debounceInterval = 300 * time.Millisecond

// WatchYAML loads path once immediately (a missing file is fine — it's
// optional, FR-RE-05), then watches it for changes and reloads
// (LoadYAMLFile) on every one, until ctx is cancelled. Runs until ctx is
// done; call it in its own goroutine.
//
// fsnotify watches path's parent directory rather than the file itself —
// the file may not exist yet at startup, and watching a directory also
// survives the common "editor saves by writing a new file and renaming it
// over the original" pattern, which replaces the original inode and would
// silently stop a direct file watch.
func WatchYAML(ctx context.Context, path string, rulesRepo *repo.RulesRepo, logger *zap.Logger) {
	reload := func() {
		loaded, err := LoadYAMLFile(ctx, path, rulesRepo)
		switch {
		case err != nil:
			logger.Error("rules: loading rules.yaml failed", zap.String("path", path), zap.Error(err))
		case loaded:
			logger.Info("rules: reloaded rules.yaml", zap.String("path", path))
		}
	}
	reload() // initial load — see LoadYAMLFile's doc comment for "missing file" handling

	watcher, err := fsnotify.NewWatcher()
	if err != nil {
		logger.Error("rules: starting rules.yaml watcher failed, changes to the file won't be picked up without a restart", zap.Error(err))
		return
	}
	defer watcher.Close()

	dir := filepath.Dir(path)
	if err := watcher.Add(dir); err != nil {
		logger.Error("rules: watching rules.yaml's directory failed, changes to the file won't be picked up without a restart",
			zap.String("dir", dir), zap.Error(err))
		return
	}

	var debounce *time.Timer
	debounced := make(chan struct{}, 1)
	defer func() {
		if debounce != nil {
			debounce.Stop()
		}
	}()

	for {
		select {
		case <-ctx.Done():
			return
		case ev, ok := <-watcher.Events:
			if !ok {
				return
			}
			if filepath.Clean(ev.Name) != filepath.Clean(path) {
				continue // some other file in the same directory
			}
			if !ev.Has(fsnotify.Write) && !ev.Has(fsnotify.Create) {
				continue // e.g. Remove/Rename/Chmod — see SyncYAML's doc comment on why deletion isn't mirrored
			}
			if debounce == nil {
				debounce = time.AfterFunc(debounceInterval, func() {
					select {
					case debounced <- struct{}{}:
					default:
					}
				})
			} else {
				debounce.Reset(debounceInterval)
			}
		case <-debounced:
			reload()
		case werr, ok := <-watcher.Errors:
			if !ok {
				return
			}
			logger.Warn("rules: rules.yaml watcher error", zap.Error(werr))
		}
	}
}
