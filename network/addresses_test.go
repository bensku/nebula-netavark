package network

import (
	"net/netip"
	"runtime"
	"slices"
	"testing"

	"github.com/vishvananda/netlink"
	"github.com/vishvananda/netns"
)

// Runs f in a new, temporary netns. Needs CAP_SYS_ADMIN, e.g. unshare -rn go test ./...
func inTempNetns(t *testing.T, f func()) {
	t.Helper()
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()
	origns, err := netns.Get()
	if err != nil {
		t.Fatal(err)
	}
	defer origns.Close()
	newns, err := netns.New()
	if err != nil {
		t.Skipf("cannot create netns (run in unshare -rn for this test): %v", err)
	}
	defer newns.Close()
	defer func() {
		if err := netns.Set(origns); err != nil {
			panic(err)
		}
	}()
	lo, err := netlink.LinkByName("lo")
	if err != nil {
		t.Fatal(err)
	}
	if err := netlink.LinkSetUp(lo); err != nil {
		t.Fatal(err)
	}
	f()
}

func loopbackAddrs(t *testing.T) []netip.Prefix {
	t.Helper()
	lo, err := netlink.LinkByName("lo")
	if err != nil {
		t.Fatal(err)
	}
	list, err := netlink.AddrList(lo, netlink.FAMILY_ALL)
	if err != nil {
		t.Fatal(err)
	}
	var addrs []netip.Prefix
	for _, a := range list {
		addr, _ := netip.AddrFromSlice(a.IP)
		bits, _ := a.Mask.Size()
		addrs = append(addrs, netip.PrefixFrom(addr.Unmap(), bits))
	}
	return addrs
}

func TestSyncUnsafeAddresses(t *testing.T) {
	inTempNetns(t, func() {
		v6 := netip.MustParsePrefix("fd42:7669::1/128")
		v6b := netip.MustParsePrefix("fd42:7669::2/128")
		v4 := netip.MustParsePrefix("10.99.0.1/32")
		loopback := netip.MustParsePrefix("::1/128")
		check := func(want, gone []netip.Prefix) {
			t.Helper()
			have := loopbackAddrs(t)
			for _, p := range want {
				if !slices.Contains(have, p) {
					t.Errorf("%s missing from loopback: %v", p, have)
				}
			}
			for _, p := range gone {
				if slices.Contains(have, p) {
					t.Errorf("%s still on loopback: %v", p, have)
				}
			}
		}

		if err := syncUnsafeAddresses(nil, []netip.Prefix{v6, v4}); err != nil {
			t.Fatal(err)
		}
		check([]netip.Prefix{v6, v4, loopback}, nil)

		if err := syncUnsafeAddresses([]netip.Prefix{v6, v4}, []netip.Prefix{v6, v6b}); err != nil {
			t.Fatal(err)
		}
		check([]netip.Prefix{v6, v6b, loopback}, []netip.Prefix{v4})

		// Addresses removed behind our back are not an error
		if err := syncUnsafeAddresses([]netip.Prefix{v6, v6b, v4}, nil); err != nil {
			t.Fatal(err)
		}
		check([]netip.Prefix{loopback}, []netip.Prefix{v6, v6b, v4})
	})
}
