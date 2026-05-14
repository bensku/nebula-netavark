package network

import (
	"fmt"
	"log/slog"
	"net/netip"

	"github.com/slackhq/nebula"
	"github.com/slackhq/nebula/config"
	"github.com/slackhq/nebula/overlay"
)

// -ldflags "-X nebula.Build=SOMEVERSION"
var Build string

type tunnelManager struct {
	logger *slog.Logger
}

// Starts Nebula endpoint, loading config from cfg and putting TUN to given netns
func (tunnels *tunnelManager) start(cfg string, netns string) (*nebula.Control, error) {
	c := config.NewC(tunnels.logger)
	err := c.Load(cfg)
	if err != nil {
		return nil, fmt.Errorf("failed to load Nebula config %s: %w", cfg, err)
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

	return ctrl, nil
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
