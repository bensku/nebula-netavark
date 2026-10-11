package network

import (
	"fmt"
	"maps"
	"slices"
)

// Root config keys for unsafe routes that can be picked by name
// Routes with default: true are always added, others only when listed in enable_routes
const (
	namedRoutesKey  = "named_routes"
	enableRoutesKey = "enable_routes"
)

// Appends selected named routes to tun.unsafe_routes and removes our keys from settings
func expandNamedRoutes(settings map[string]any) error {
	rawRoutes, rawEnabled := settings[namedRoutesKey], settings[enableRoutesKey]
	delete(settings, namedRoutesKey)
	delete(settings, enableRoutesKey)

	var routes map[string]any
	if rawRoutes != nil {
		var ok bool
		if routes, ok = rawRoutes.(map[string]any); !ok {
			return fmt.Errorf("%s must be a map of routes", namedRoutesKey)
		}
	}
	enabled, err := parseEnabledRoutes(rawEnabled)
	if err != nil {
		return err
	}
	for _, name := range enabled {
		// Null lets endpoints remove routes they import, so it doesn't count as defined
		if routes[name] == nil {
			return fmt.Errorf("%s: route %q is not in %s", enableRoutesKey, name, namedRoutesKey)
		}
	}

	var selected []any
	// Sorted so that unchanged config produces identical Nebula settings
	for _, name := range slices.Sorted(maps.Keys(routes)) {
		if routes[name] == nil {
			continue
		}
		route, ok := routes[name].(map[string]any)
		if !ok {
			return fmt.Errorf("%s: route %q must be a map", namedRoutesKey, name)
		}
		isDefault := false
		if raw, exists := route["default"]; exists {
			if isDefault, ok = raw.(bool); !ok {
				return fmt.Errorf("%s: route %q: default must be true or false", namedRoutesKey, name)
			}
		}
		if !isDefault && !slices.Contains(enabled, name) {
			continue
		}
		route = maps.Clone(route)
		delete(route, "default")
		selected = append(selected, route)
	}
	if len(selected) == 0 {
		return nil
	}

	tun, ok := settings["tun"].(map[string]any)
	if !ok {
		if settings["tun"] != nil {
			return fmt.Errorf("tun must be a map")
		}
		tun = make(map[string]any)
		settings["tun"] = tun
	}
	var unsafeRoutes []any
	if raw := tun["unsafe_routes"]; raw != nil {
		if unsafeRoutes, ok = raw.([]any); !ok {
			return fmt.Errorf("tun.unsafe_routes must be a list")
		}
	}
	tun["unsafe_routes"] = append(slices.Clone(unsafeRoutes), selected...)
	return nil
}

func parseEnabledRoutes(raw any) ([]string, error) {
	if raw == nil {
		return nil, nil
	}
	values, ok := raw.([]any)
	if !ok {
		return nil, fmt.Errorf("%s must be a list of route names", enableRoutesKey)
	}
	var names []string
	for _, value := range values {
		name, ok := value.(string)
		if !ok {
			return nil, fmt.Errorf("%s must be a list of route names", enableRoutesKey)
		}
		names = append(names, name)
	}
	return names, nil
}
