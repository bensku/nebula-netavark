package network

import (
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"sync"

	"github.com/slackhq/nebula/logging"
)

// Manager for Nebula endpoints
type Manager struct {
	configDir string
	tunnels   tunnelManager
	db        endpointDb

	endpoints sync.Map
	lifecycle sync.RWMutex
	closed    bool
	stopWatch chan struct{}
	watchDone <-chan struct{}
}

// EndpointInfo describes the interface and addresses actually assigned by Nebula.
// This information is needed by podman inspect
type EndpointInfo struct {
	InterfaceName string
	Addresses     []netip.Prefix
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

	// Start watching before endpoints come up, so we don't miss changes
	mgr.stopWatch = make(chan struct{})
	mgr.watchDone = watchConfigDir(configDir, mgr.tunnels.logger, mgr.reload, mgr.stopWatch)

	// Reconnect all existing endpoints
	endpoints, err := mgr.db.List()
	if err != nil {
		mgr.Close()
		return nil, err
	}
	for container, netns := range endpoints {
		if _, err := mgr.Up(container, netns); err != nil {
			slog.Warn("failed to reconnect endpoint, maybe container no longer exists", "container", container, "error", err)
			_ = mgr.db.Remove(container) // Try to remove it from DB to prevent this error from reoccurring
		}
	}
	return mgr, nil
}

// Brings an endpoint up for given container netns
func (mgr *Manager) Up(container string, netns string) (EndpointInfo, error) {
	mgr.lifecycle.RLock()
	defer mgr.lifecycle.RUnlock()
	if mgr.closed {
		return EndpointInfo{}, fmt.Errorf("manager closed")
	}
	if err := validateContainerId(container); err != nil {
		return EndpointInfo{}, err
	}
	cfg := filepath.Join(mgr.configDir, container+".yml")
	if _, err := os.Stat(cfg); err != nil {
		return EndpointInfo{}, fmt.Errorf("could not load config %s: %w", cfg, err)
	}

	// Double-call protection
	_, loaded := mgr.endpoints.LoadOrStore(container, "marker")
	if loaded {
		return EndpointInfo{}, fmt.Errorf("endpoint already exists")
	}

	// Start up Nebula endpoint
	endpoint, err := mgr.tunnels.start(mgr.configDir, container+".yml", netns)
	if err != nil {
		mgr.endpoints.Delete(container) // Clear our marker
		return EndpointInfo{}, err
	}

	// Store to FS so that it will come back up after we restart (due to upgrade, crash, etc.)
	// We intentionally do this AFTER Nebula successfully starts
	// because if it failed, we'd return failure to netavark and the container creation would fail, too
	if err := mgr.db.Add(container, netns); err != nil {
		endpoint.stop() // It already started, ensure it stops (async) to avoid leaking resources
		mgr.endpoints.Delete(container)
		return EndpointInfo{}, err
	}

	mgr.endpoints.Store(container, endpoint) // Replace marker with real endpoint
	// Config may have changed after we loaded it, but before reload could see this endpoint
	endpoint.reloadIfChanged(mgr.tunnels.logger)
	device := endpoint.ctrl.Device()
	return EndpointInfo{
		InterfaceName: device.Name(),
		Addresses:     append([]netip.Prefix(nil), device.Networks()...),
	}, nil
}

// Takes an endpoint down for given container, if it was up
func (mgr *Manager) Down(container string) error {
	// Exclude starts and shutdown while stopping
	mgr.lifecycle.Lock()
	defer mgr.lifecycle.Unlock()
	if err := validateContainerId(container); err != nil {
		return err
	}
	val, ok := mgr.endpoints.Load(container)
	if val == "marker" {
		return fmt.Errorf("Down called for %s while Up is in flight", container)
	}
	if err := mgr.db.Remove(container); err != nil {
		return err
	}
	if !ok {
		return nil
	}
	endpoint := val.(*runningEndpoint)
	endpoint.stop()
	mgr.endpoints.Delete(container)
	return nil
}

// Close stops config watching and live endpoints but keeps restart recovery records.
func (mgr *Manager) Close() {
	mgr.lifecycle.Lock()
	defer mgr.lifecycle.Unlock()
	if mgr.closed {
		return
	}
	mgr.closed = true
	close(mgr.stopWatch)
	<-mgr.watchDone
	mgr.endpoints.Range(func(key, value any) bool {
		value.(*runningEndpoint).stop()
		mgr.endpoints.Delete(key)
		return true
	})
}

// Reloads configs of all endpoints whose config files have changed
func (mgr *Manager) reload() {
	mgr.endpoints.Range(func(key, value any) bool {
		if endpoint, ok := value.(*runningEndpoint); ok { // Skip markers
			endpoint.reloadIfChanged(mgr.tunnels.logger)
		}
		return true
	})
}
