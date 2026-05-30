package network

import (
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/slackhq/nebula"
	"github.com/slackhq/nebula/logging"
)

// Manager for Nebula endpoints
type Manager struct {
	configDir string
	tunnels   tunnelManager
	db        endpointDb

	endpoints sync.Map
}

// Loads manager
// Nebula network configs should be in configDir
// We'll load and store endpoints in stateDir for restart/crash safety
func LoadManager(configDir string, stateDir string) (*Manager, error) {
	mgr := &Manager{
		configDir: configDir,
		tunnels: tunnelManager{
			logger: logging.NewLogger(os.Stdout),
		},
		db: endpointDb{
			path: stateDir,
		},
		endpoints: sync.Map{},
	}

	// Reconnect all existing endpoints
	endpoints, err := mgr.db.List()
	if err != nil {
		return nil, err
	}
	for container, netns := range endpoints {
		if err := mgr.Up(container, netns); err != nil {
			slog.Warn("failed to reconnect endpoint, maybe container no longer exists", "container", container, "error", err)
			_ = mgr.db.Remove(container) // Try to remove it from DB to prevent this error from reoccurring
		}
	}
	return mgr, nil
}

// Brings an endpoint up for given container netns
func (mgr *Manager) Up(container string, netns string) error {
	cfg := filepath.Join(mgr.configDir, container+".yml")
	if _, err := os.Stat(cfg); err != nil {
		return fmt.Errorf("could not load config %s: %w", cfg, err)
	}

	// Double-call protection
	_, loaded := mgr.endpoints.LoadOrStore(container, "marker")
	if loaded {
		return fmt.Errorf("endpoint already exists")
	}

	// Start up Nebula endpoint
	ctrl, err := mgr.tunnels.start(cfg, netns)
	if err != nil {
		mgr.endpoints.Delete(container) // Clear our marker
		return err
	}
	mgr.endpoints.Store(container, ctrl) // Replace marker with real ctrl

	// Store to FS so that it will come back up after we restart (due to upgrade, crash, etc.)
	// We intentionally do this AFTER Nebula successfully starts
	// because if it failed, we'd return failure to netavark and the container creation would fail, too
	if err := mgr.db.Add(container, netns); err != nil {
		ctrl.Stop() // It already started, ensure it stops (async) to avoid leaking resources
		mgr.endpoints.Delete(container)
		return err
	}

	return nil
}

// Takes an endpoint down for given container, if it was up
func (mgr *Manager) Down(container string) error {
	if err := mgr.db.Remove(container); err != nil {
		// TODO is it ever good idea to deny endpoint deletion?
		return err
	}

	val, ok := mgr.endpoints.Load(container)
	if !ok {
		return nil // Not up, so it is already down. Hopefully.
	}
	if val == "marker" {
		return fmt.Errorf("Down called for %s while Up is in flight", container)
	}
	deleted := mgr.endpoints.CompareAndDelete(container, val)
	if deleted {
		val.(*nebula.Control).Stop()
	} // else: racing Down calls, probably

	return nil
}
