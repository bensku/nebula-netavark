package network

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestReservedContainerNames(t *testing.T) {
	for _, name := range []string{"_host", "_anything"} {
		if err := validateContainerId(name); err == nil {
			t.Errorf("container id %q was accepted", name)
		}
	}
	if err := validateContainerId("host"); err != nil {
		t.Errorf("container id \"host\" was rejected: %v", err)
	}
}

// Host endpoints that can't start must not take containers down with them
func TestBrokenHostConfig(t *testing.T) {
	for _, tc := range []struct {
		name     string
		contents string
		wantErr  string
	}{
		{"malformed YAML", "pki: [\n", "failed to load Nebula config"},
		{"no certificates", "listen: {port: 0}\n", "failed to initialize"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			configDir := filepath.Join(dir, "config")
			writeConfig(t, configDir, hostConfig, tc.contents)
			mgr, err := LoadManager(configDir, filepath.Join(dir, "state"))
			if err != nil {
				t.Fatal(err)
			}
			defer mgr.Close()
			if mgr.host != nil {
				t.Fatal("host endpoint started with broken config")
			}
			if !strings.Contains(mgr.hostErr, tc.wantErr) {
				t.Fatalf("host error = %q, want %q", mgr.hostErr, tc.wantErr)
			}

			// Removing config clears the error, so it is logged again if it comes back
			if err := os.Remove(filepath.Join(configDir, hostConfig)); err != nil {
				t.Fatal(err)
			}
			mgr.syncHost()
			if mgr.host != nil || mgr.hostErr != "" {
				t.Fatalf("host = %v, error = %q after config removal", mgr.host, mgr.hostErr)
			}
		})
	}
}

func TestNoHostConfig(t *testing.T) {
	dir := t.TempDir()
	mgr, err := LoadManager(filepath.Join(dir, "config"), filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	defer mgr.Close()
	if mgr.host != nil || mgr.hostErr != "" {
		t.Fatalf("host = %v, error = %q without host config", mgr.host, mgr.hostErr)
	}
	if _, err := mgr.Up("_host", "/missing/netns"); err == nil {
		t.Fatal("Up accepted reserved container name")
	}
}
