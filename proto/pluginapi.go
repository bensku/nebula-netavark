// Package proto defines the JSON types exchanged with netavark over a plugin
// binary's stdin/stdout, as documented at
// https://github.com/containers/netavark/blob/main/plugin-API.md
package proto

import (
	"encoding/json"
	"net/netip"
	"time"
)

// APIVersion is the netavark plugin API version this package targets.
const APIVersion = "1.0.0"

// Network is the network configuration. It is the input and output of the
// `create` subcommand, and is nested inside [NetworkPluginExec] for
// `setup`/`teardown`.
type Network struct {
	Created           *time.Time        `json:"created,omitempty"`
	DNSEnabled        bool              `json:"dns_enabled"`
	Driver            string            `json:"driver"`
	ID                string            `json:"id"`
	Internal          bool              `json:"internal"`
	IPv6Enabled       bool              `json:"ipv6_enabled"`
	Name              string            `json:"name"`
	NetworkInterface  *string           `json:"network_interface,omitempty"`
	Options           map[string]string `json:"options,omitempty"`
	IPAMOptions       map[string]string `json:"ipam_options,omitempty"`
	Subnets           []Subnet          `json:"subnets,omitempty"`
	Routes            []Route           `json:"routes,omitempty"`
	NetworkDNSServers []netip.Addr      `json:"network_dns_servers,omitempty"`
	Labels            map[string]string `json:"labels,omitempty"`
}

// Subnet is a single subnet attached to a [Network].
type Subnet struct {
	Gateway    *netip.Addr  `json:"gateway,omitempty"`
	LeaseRange *LeaseRange  `json:"lease_range,omitempty"`
	Subnet     netip.Prefix `json:"subnet"`
}

// LeaseRange bounds the IPs that IPAM is allowed to hand out for a subnet.
type LeaseRange struct {
	StartIP *string `json:"start_ip,omitempty"`
	EndIP   *string `json:"end_ip,omitempty"`
}

// Route is a static route attached to a [Network].
type Route struct {
	Destination netip.Prefix `json:"destination"`
	// Gateway is required for unicast routes and must not be set for
	// blackhole/unreachable/prohibit routes.
	Gateway *netip.Addr `json:"gateway,omitempty"`
	Metric  *uint32     `json:"metric,omitempty"`
	// RouteType accepts the same values as `ip route`: unicast, blackhole,
	// unreachable, prohibit. Defaults to unicast when unset.
	RouteType *string `json:"route_type,omitempty"`
}

// NetworkPluginExec is the stdin payload for the `setup` and `teardown`
// subcommands.
type NetworkPluginExec struct {
	ContainerID    string            `json:"container_id"`
	ContainerName  string            `json:"container_name"`
	PortMappings   []PortMapping     `json:"port_mappings,omitempty"`
	Network        Network           `json:"network"`
	NetworkOptions PerNetworkOptions `json:"network_options"`
}

// PerNetworkOptions are the per-container settings for one network attachment.
type PerNetworkOptions struct {
	// Aliases the DNS server should resolve to this container. Should only be
	// set when DNSEnabled is true on the [Network]; plugins without DNS
	// support must ignore (not error on) aliases.
	Aliases       []string          `json:"aliases,omitempty"`
	InterfaceName string            `json:"interface_name"`
	StaticIPs     []netip.Addr      `json:"static_ips,omitempty"`
	StaticMAC     *string           `json:"static_mac,omitempty"`
	Options       map[string]string `json:"options,omitempty"`
}

// PortMapping is one or more host:container port forwards.
type PortMapping struct {
	ContainerPort uint16 `json:"container_port"`
	// HostIP is the host bind address. Empty means 0.0.0.0.
	HostIP   string `json:"host_ip"`
	HostPort uint16 `json:"host_port"`
	// Protocol is "tcp", "udp", "sctp", or a comma-separated combination.
	// Empty defaults to tcp.
	Protocol string `json:"protocol"`
	// Range is 1-indexed: 1 maps a single port, 2 maps two consecutive ports,
	// etc. HostPort+Range and ContainerPort+Range must both be < 65536.
	Range uint16 `json:"range"`
}

// StatusBlock is the stdout payload returned by the `setup` subcommand.
type StatusBlock struct {
	DNSSearchDomains []string                `json:"dns_search_domains,omitempty"`
	DNSServerIPs     []netip.Addr            `json:"dns_server_ips,omitempty"`
	Interfaces       map[string]NetInterface `json:"interfaces,omitempty"`
}

// NetInterface describes one interface created inside the container netns.
type NetInterface struct {
	MACAddress string       `json:"mac_address"`
	Subnets    []NetAddress `json:"subnets,omitempty"`
}

// NetAddress is one IP assignment on a [NetInterface]. IPNet must contain the
// interface IP itself, not the bare network address.
type NetAddress struct {
	Gateway *netip.Addr  `json:"gateway,omitempty"`
	IPNet   netip.Prefix `json:"ipnet"`
}

// Info is the stdout payload of the `info` subcommand. ExtraInfo entries are
// serialised flattened into the top-level JSON object.
type Info struct {
	Version    string            `json:"version"`
	APIVersion string            `json:"api_version"`
	ExtraInfo  map[string]string `json:"-"`
}

// MarshalJSON implements [json.Marshaler], flattening ExtraInfo into the
// top-level object to match netavark's serde(flatten) layout.
func (i Info) MarshalJSON() ([]byte, error) {
	out := make(map[string]string, 2+len(i.ExtraInfo))
	for k, v := range i.ExtraInfo {
		out[k] = v
	}
	out["version"] = i.Version
	out["api_version"] = i.APIVersion
	return json.Marshal(out)
}

// Error is the stdout payload a plugin writes on failure: {"error": "..."}.
type Error struct {
	Error string `json:"error"`
}
