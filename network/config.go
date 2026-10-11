package network

import (
	"fmt"
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"

	"github.com/slackhq/nebula/config"
	"go.yaml.in/yaml/v3"
)

// Imports deeper than this are almost certainly cycles
const maxImportDepth = 8

// Loads config file name from dir, expanding imports
// Also returns the merged Nebula settings as YAML, so callers can detect changes,
// and unsafe addresses which are not given to Nebula
// Named routes are expanded into tun.unsafe_routes
func loadConfig(dir, name string, logger *slog.Logger) (*config.C, string, []netip.Prefix, error) {
	settings, err := loadSettings(dir, name, 0)
	if err != nil {
		return nil, "", nil, err
	}
	addrs, err := parseUnsafeAddresses(settings[unsafeAddressesKey])
	if err != nil {
		return nil, "", nil, fmt.Errorf("config %s: %w", name, err)
	}
	delete(settings, unsafeAddressesKey)
	if err := expandNamedRoutes(settings); err != nil {
		return nil, "", nil, fmt.Errorf("config %s: %w", name, err)
	}
	raw, err := yaml.Marshal(settings)
	if err != nil {
		return nil, "", nil, err
	}
	// Pathless configs prevent Nebula from installing a SIGHUP handler which
	// would bypass import expansion; we reload on file changes instead.
	c := config.NewC(logger)
	c.Settings = settings
	return c, string(raw), addrs, nil
}

func loadSettings(dir, name string, depth int) (map[string]any, error) {
	if depth > maxImportDepth {
		return nil, fmt.Errorf("config %s: imports nested too deeply (cycle?)", name)
	}
	data, err := os.ReadFile(filepath.Join(dir, name))
	if err != nil {
		return nil, fmt.Errorf("read config %s: %w", name, err)
	}
	var settings map[string]any
	if err := yaml.Unmarshal(data, &settings); err != nil {
		return nil, fmt.Errorf("config %s: %w", name, err)
	}
	raw, exists := settings["import"]
	if !exists {
		return settings, nil
	}
	imports, err := configImports(raw)
	if err != nil {
		return nil, fmt.Errorf("config %s: %w", name, err)
	}
	delete(settings, "import")

	// Import paths are relative to config dir, not the importing file
	merged := make(map[string]any)
	for _, imported := range imports {
		baseline, err := loadSettings(dir, imported, depth+1)
		if err != nil {
			return nil, fmt.Errorf("config %s imports %s: %w", name, imported, err)
		}
		mergeConfig(merged, baseline)
	}
	mergeConfig(merged, settings)
	return merged, nil
}

func configImports(raw any) ([]string, error) {
	var names []string
	if name, ok := raw.(string); ok {
		names = []string{name}
	} else if values, ok := raw.([]any); ok {
		for _, value := range values {
			name, ok := value.(string)
			if !ok {
				return nil, fmt.Errorf("import must be a path or a list of paths")
			}
			names = append(names, name)
		}
	} else {
		return nil, fmt.Errorf("import must be a path or a list of paths")
	}
	for _, name := range names {
		// Only files under config dir are watched for changes
		if !filepath.IsLocal(name) {
			return nil, fmt.Errorf("import %q must be a relative path inside config directory", name)
		}
	}
	return names, nil
}

// Maps merge recursively; all other values (including lists, zero values and
// null) replace the baseline. Copy maps so sibling imports cannot mutate them.
func mergeConfig(dst, src map[string]any) {
	for key, value := range src {
		if incoming, ok := value.(map[string]any); ok {
			existing, ok := dst[key].(map[string]any)
			if !ok {
				existing = make(map[string]any)
			}
			mergeConfig(existing, incoming)
			dst[key] = existing
		} else {
			dst[key] = value
		}
	}
}
