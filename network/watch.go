package network

import (
	"io/fs"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/fsnotify/fsnotify"
)

// Deployment tools often write several files at once; reload once after them
const reloadDebounce = 250 * time.Millisecond

// Watches dir and all its subdirectories, calling onChange after anything in them changes
// onChange may be called spuriously; it should be cheap when nothing actually changed
func watchConfigDir(dir string, logger *slog.Logger, onChange func(), stop <-chan struct{}) <-chan struct{} {
	done := make(chan struct{})
	watcher, err := fsnotify.NewWatcher()
	if err == nil {
		err = watcher.Add(dir)
		if err != nil {
			watcher.Close()
		}
	}
	if err != nil {
		logger.Warn("cannot watch config directory, config changes will not be applied until restart", "directory", dir, "error", err)
		close(done)
		return done
	}

	// fsnotify is not recursive, so every subdirectory needs its own watch
	// Symlinked directories are not supported
	addTree := func(root string) {
		_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
			if err == nil && entry.IsDir() {
				if err := watcher.Add(path); err != nil {
					logger.Debug("cannot watch config subdirectory", "directory", path, "error", err)
				}
			}
			return nil // Keep going, even if parts of the tree are unreadable
		})
	}
	addTree(dir)

	go func() {
		defer close(done)
		defer watcher.Close()
		timer := time.NewTimer(reloadDebounce)
		timer.Stop()
		defer timer.Stop()
		for {
			select {
			case <-stop:
				return
			case event := <-watcher.Events:
				if event.Has(fsnotify.Create) {
					if info, err := os.Lstat(event.Name); err == nil && info.IsDir() {
						// New (or renamed) directory, possibly with files already in it
						addTree(event.Name)
					}
				}
				// Removed directories lose their watches automatically
				timer.Reset(reloadDebounce)
			case err := <-watcher.Errors:
				// Probably event queue overflow; we may have missed anything
				logger.Warn("config watcher error, reloading everything", "error", err)
				addTree(dir)
				onChange()
			case <-timer.C:
				onChange()
			}
		}
	}()
	return done
}
