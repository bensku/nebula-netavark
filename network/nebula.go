package network

import (
	"errors"
	"fmt"
	"log/slog"
	"net/netip"
	"sync"

	"github.com/slackhq/nebula"
	"github.com/slackhq/nebula/config"
	"github.com/slackhq/nebula/overlay"
)

// -ldflags "-X nebula.Build=SOMEVERSION"
var Build string

type tunnelManager struct {
	logger *slog.Logger
}

// A Nebula endpoint that is up in some container's netns
type runningEndpoint struct {
	mu      sync.Mutex
	stopped bool
	ctrl    *nebula.Control
	config  *config.C
	netns   string
	dir     string
	name    string
	raw     string // Currently applied config
	lastErr string // Last config error, to avoid logging it repeatedly
}

// Reloads endpoint config from disk if its effective content has changed
func (endpoint *runningEndpoint) reloadIfChanged(logger *slog.Logger) {
	endpoint.mu.Lock()
	defer endpoint.mu.Unlock()
	if endpoint.stopped {
		return
	}
	_, raw, err := loadConfig(endpoint.dir, endpoint.name, logger)
	if err == nil && raw != endpoint.raw {
		var enterErr error
		_, err, enterErr = netnsExec(endpoint.netns, func() (struct{}, error) {
			return struct{}{}, endpoint.config.ReloadConfigString(raw)
		})
		err = errors.Join(enterErr, err)
		if err == nil {
			endpoint.raw = raw
			logger.Info("reloaded endpoint config", "config", endpoint.name)
		}
	}
	if err != nil {
		// Keep using the previous config until this is fixed
		if err.Error() != endpoint.lastErr {
			logger.Warn("failed to reload endpoint config", "config", endpoint.name, "error", err)
		}
		endpoint.lastErr = err.Error()
	} else {
		endpoint.lastErr = ""
	}
}

func (endpoint *runningEndpoint) stop() {
	endpoint.mu.Lock()
	defer endpoint.mu.Unlock()
	endpoint.stopped = true
	endpoint.ctrl.Stop()
}

// Starts Nebula endpoint, loading config name from dir and putting TUN to given netns
func (tunnels *tunnelManager) start(dir string, name string, netns string) (*runningEndpoint, error) {
	c, raw, err := loadConfig(dir, name, tunnels.logger)
	if err != nil {
		return nil, fmt.Errorf("failed to load Nebula config in %s: %w", dir, err)
	}

	ctrl, err := nebula.Main(c, false, Build, tunnels.logger, namespacedFactory(netns))
	if err != nil {
		return nil, fmt.Errorf("failed to initialize: %w", err)
	}

	_, err, enterErr := netnsExec(netns, func() (func() error, error) {
		return ctrl.Start()
	})
	if enterErr != nil {
		return nil, fmt.Errorf("failed to enter netns for endpoint start: %w", enterErr)
	}
	if err != nil {
		return nil, fmt.Errorf("failed to start endpoint: %w", err)
	}

	return &runningEndpoint{ctrl: ctrl, config: c, netns: netns, dir: dir, name: name, raw: raw}, nil
}

func namespacedFactory(netns string) overlay.DeviceFactory {
	return func(c *config.C, l *slog.Logger, vpnNetworks []netip.Prefix, routines int) (overlay.Device, error) {
		res, err, enterErr := netnsExec(netns, func() (overlay.Device, error) {
			return overlay.NewDeviceFromConfig(c, l, vpnNetworks, routines)
		})
		if enterErr != nil {
			return nil, fmt.Errorf("failed to enter netns for TUN create: %w", enterErr)
		}
		// Normal TUN creation errors don't need further explanation from here
		return res, err
	}
}
