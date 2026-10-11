package network

import (
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/netip"
	"slices"

	"github.com/slackhq/nebula"
	"github.com/vishvananda/netlink"
	"golang.org/x/sys/unix"
)

// Root config key for addresses added to loopback in endpoint's netns
// Clients reach them through unsafe_routes that point to this endpoint
const unsafeAddressesKey = "unsafe_addresses"

// Parses unsafe_addresses value: a list of addresses, optionally with full-length prefix
func parseUnsafeAddresses(raw any) ([]netip.Prefix, error) {
	if raw == nil {
		return nil, nil
	}
	values, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be a list of addresses", unsafeAddressesKey)
	}
	var addrs []netip.Prefix
	for _, value := range values {
		str, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("%s must be a list of addresses", unsafeAddressesKey)
		}
		prefix, err := netip.ParsePrefix(str)
		if err != nil {
			addr, addrErr := netip.ParseAddr(str)
			if addrErr != nil || addr.Zone() != "" {
				return nil, fmt.Errorf("%s: invalid address %q", unsafeAddressesKey, str)
			}
			prefix = netip.PrefixFrom(addr, addr.BitLen())
		}
		// On loopback, a shorter prefix would make whole subnet local
		if !prefix.IsSingleIP() {
			return nil, fmt.Errorf("%s: %q must be a single address", unsafeAddressesKey, str)
		}
		prefix = netip.PrefixFrom(prefix.Addr().Unmap(), prefix.Addr().Unmap().BitLen())
		if !slices.Contains(addrs, prefix) {
			addrs = append(addrs, prefix)
		}
	}
	return addrs, nil
}

// Adds addresses in want but not in have to loopback, then removes addresses only in have
// Must be called in endpoint's netns
func syncUnsafeAddresses(have, want []netip.Prefix) error {
	lo, err := netlink.LinkByName("lo")
	if err != nil {
		return fmt.Errorf("failed to find loopback: %w", err)
	}
	for _, prefix := range want {
		if err := netlink.AddrReplace(lo, loopbackAddr(prefix)); err != nil {
			return fmt.Errorf("failed to add %s to loopback: %w", prefix, err)
		}
	}
	var errs []error
	for _, prefix := range have {
		if slices.Contains(want, prefix) {
			continue
		}
		err := netlink.AddrDel(lo, loopbackAddr(prefix))
		if err != nil && !errors.Is(err, unix.EADDRNOTAVAIL) {
			errs = append(errs, fmt.Errorf("failed to remove %s from loopback: %w", prefix, err))
		}
	}
	return errors.Join(errs...)
}

func loopbackAddr(prefix netip.Prefix) *netlink.Addr {
	return &netlink.Addr{IPNet: &net.IPNet{
		IP:   prefix.Addr().AsSlice(),
		Mask: net.CIDRMask(prefix.Bits(), prefix.Addr().BitLen()),
	}}
}

// Warns about addresses that Nebula won't accept traffic for, because our certificate
// doesn't list them in unsafe networks
func warnUncoveredAddresses(logger *slog.Logger, name string, ctrl *nebula.Control, addrs []netip.Prefix) {
	networks := ctrl.Device().Networks()
	if len(addrs) == 0 || len(networks) == 0 {
		return
	}
	crt := ctrl.GetCertByVpnIp(networks[0].Addr())
	if crt == nil {
		return
	}
	for _, prefix := range addrs {
		if !slices.ContainsFunc(crt.UnsafeNetworks(), func(n netip.Prefix) bool { return n.Contains(prefix.Addr()) }) {
			logger.Warn("unsafe address is not in certificate's unsafe networks, Nebula will drop its traffic",
				"config", name, "address", prefix.Addr())
		}
	}
}
