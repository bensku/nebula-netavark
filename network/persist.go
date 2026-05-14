package network

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

type endpointDb struct {
	path string // Directory where we store marker files
}

func (db *endpointDb) List() (map[string]string, error) {
	if err := os.MkdirAll(db.path, 0o755); err != nil {
		return nil, fmt.Errorf("failed to create state dir %s: %w", db.path, err)
	}

	entries, err := os.ReadDir(db.path)
	if err != nil {
		return nil, fmt.Errorf("failed to read state dir %s: %w", db.path, err)
	}

	out := make(map[string]string, len(entries))
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if strings.Contains(e.Name(), ".tmp-") {
			// Leftover temp file from crash
			// Try clearing it, but if we fail, just keep going
			os.Remove(filepath.Join(db.path, e.Name()))
			continue
		}
		container := e.Name()
		netns, err := os.ReadFile(filepath.Join(db.path, container))
		if err != nil {
			return nil, fmt.Errorf("failed to read endpoint %s: %w", container, err)
		}
		out[container] = strings.TrimRight(string(netns), "\n")
	}
	return out, nil
}

func (db *endpointDb) Add(container string, netns string) error {
	if err := validateContainerId(container); err != nil {
		return err
	}
	if err := os.MkdirAll(db.path, 0o755); err != nil {
		return fmt.Errorf("failed to create state dir %s: %w", db.path, err)
	}

	// Write atomically via tmp file + rename so a process crash mid-write can't leave a partial entry
	full := filepath.Join(db.path, container)
	tmp, err := os.CreateTemp(db.path, container+".tmp-*")
	if err != nil {
		return fmt.Errorf("failed to create tmp file: %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.WriteString(netns); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("failed to write endpoint %s: %w", container, err)
	}
	if err := tmp.Close(); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("failed to close endpoint %s: %w", container, err)
	}
	if err := os.Rename(tmpName, full); err != nil {
		os.Remove(tmpName)
		return fmt.Errorf("failed to persist endpoint %s: %w", container, err)
	}
	return nil
}

func (db *endpointDb) Remove(container string) error {
	if err := validateContainerId(container); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(db.path, container)); err != nil && !errors.Is(err, os.ErrNotExist) {
		return fmt.Errorf("failed to remove endpoint %s: %w", container, err)
	}
	return nil
}

func validateContainerId(container string) error {
	if container == "" || container == "." || container == ".." ||
		strings.ContainsAny(container, "/\\") || strings.Contains(container, ".tmp-") {
		return fmt.Errorf("invalid container id: %q", container)
	}
	return nil
}
