package network

import (
	"fmt"
	"runtime"

	"github.com/vishvananda/netns"
)

// Executes given function in netns at path and returns its results to caller
// Third result is error if entering the netns failed
func netnsExec[T any](path string, f func() (T, error)) (T, error, error) {
	// netns is entered for current OS thread, so runtime must not change it!
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	// Get current netns so we can switch back to it once done
	var out T // Only way to return zero value for T for error paths is this :/
	origns, err := netns.Get()
	if err != nil {
		return out, nil, fmt.Errorf("failed to get current netns (very unusual!): %w", err)
	}
	defer origns.Close()

	// Try to open new netns
	newns, err := netns.GetFromPath(path)
	if err != nil {
		return out, nil, fmt.Errorf("failed to open netns %s: %w", path, err)
	}
	defer newns.Close()

	// Enter it for current thread
	if err := netns.Set(newns); err != nil {
		return out, nil, fmt.Errorf("failed to enter netns %s: %w", path, err)
	}

	// Run stuff in netns, store its result
	// from perspective of netnsExec, it doesn't matter if f() worked or not
	// caller can decide what to do with the results, after we've exited the netns
	out, innerErr := f()

	err = netns.Set(origns)
	if err != nil {
		// If we fail to enter old netns, one OS thread will be left in some container's netns
		// This could, later, result in creation of broken tunnels that have their underlay in wrong netns
		// Best to crash and let us be restarted if this ever happens!
		panic(fmt.Errorf("failed to switch back to host netns, cannot safely continue serving Nebula: %w", err))
	}

	return out, innerErr, nil
}
