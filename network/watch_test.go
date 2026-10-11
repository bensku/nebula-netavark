package network

import (
	"io"
	"log/slog"
	"os"
	"path/filepath"
	"testing"
	"time"
)

// Starts watching dir; returned channel receives a value for every onChange call
func startWatch(t *testing.T, dir string) <-chan struct{} {
	t.Helper()
	changes := make(chan struct{}, 100)
	stop := make(chan struct{})
	done := watchConfigDir(dir, slog.New(slog.NewTextHandler(io.Discard, nil)), func() {
		changes <- struct{}{}
	}, stop)
	t.Cleanup(func() {
		close(stop)
		select {
		case <-done:
		case <-time.After(5 * time.Second):
			t.Error("watcher did not stop")
		}
	})
	return changes
}

func awaitChange(t *testing.T, changes <-chan struct{}) {
	t.Helper()
	select {
	case <-changes:
	case <-time.After(5 * time.Second):
		t.Fatal("no change detected")
	}
}

func expectQuiet(t *testing.T, changes <-chan struct{}) {
	t.Helper()
	select {
	case <-changes:
		t.Fatal("unexpected change notification")
	case <-time.After(3 * reloadDebounce):
	}
}

func TestWatchDebouncesWrites(t *testing.T) {
	dir := t.TempDir()
	changes := startWatch(t, dir)
	for i := range 5 {
		writeConfig(t, dir, "network.yml", "listen: {port: "+string(rune('0'+i))+"}\n")
	}
	awaitChange(t, changes)
	expectQuiet(t, changes)
}

func TestWatchAtomicReplace(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "network.yml", "listen: {port: 1}\n")
	changes := startWatch(t, dir)
	tmp := writeConfig(t, dir, ".network.yml.tmp", "listen: {port: 2}\n")
	if err := os.Rename(tmp, filepath.Join(dir, "network.yml")); err != nil {
		t.Fatal(err)
	}
	awaitChange(t, changes)
}

func TestWatchSubdirectories(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "existing/deep/base.yml", "listen: {port: 1}\n")
	changes := startWatch(t, dir)

	writeConfig(t, dir, "existing/deep/base.yml", "listen: {port: 2}\n")
	awaitChange(t, changes)

	// Newly created directories get watched too
	if err := os.MkdirAll(filepath.Join(dir, "new/nested"), 0o755); err != nil {
		t.Fatal(err)
	}
	awaitChange(t, changes)
	writeConfig(t, dir, "new/nested/base.yml", "listen: {port: 3}\n")
	awaitChange(t, changes)

	// And so do directories that are moved into place with files already in them
	staging := t.TempDir()
	writeConfig(t, staging, "moved/base.yml", "listen: {port: 4}\n")
	if err := os.Rename(filepath.Join(staging, "moved"), filepath.Join(dir, "moved")); err != nil {
		t.Fatal(err)
	}
	awaitChange(t, changes)
	writeConfig(t, dir, "moved/base.yml", "listen: {port: 5}\n")
	awaitChange(t, changes)
}
