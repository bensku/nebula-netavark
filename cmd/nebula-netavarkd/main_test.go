package main

import (
	"encoding/json"
	"io"
	"net"
	"net/netip"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"github.com/bensku/nebula-netavark/network"
	"github.com/bensku/nebula-netavark/proto"
)

func TestEndpointStatusIPv6(t *testing.T) {
	endpoint := network.EndpointInfo{
		InterfaceName: "nebula-test",
		Addresses: []netip.Prefix{
			netip.MustParsePrefix("fd42:6e62::2/64"),
			netip.MustParsePrefix("fd42:6e63::2/96"),
		},
	}
	data, err := json.Marshal(endpointStatus(endpoint))
	if err != nil {
		t.Fatal(err)
	}
	var decoded proto.StatusBlock
	if err := json.Unmarshal(data, &decoded); err != nil {
		t.Fatal(err)
	}
	iface, ok := decoded.Interfaces[endpoint.InterfaceName]
	if !ok || len(decoded.Interfaces) != 1 || iface.MACAddress != "" {
		t.Fatalf("unexpected interface metadata: %s", data)
	}
	var addresses []netip.Prefix
	for _, address := range iface.Subnets {
		if address.Gateway != nil {
			t.Fatalf("unexpected gateway: %s", data)
		}
		addresses = append(addresses, address.IPNet)
	}
	if !reflect.DeepEqual(addresses, endpoint.Addresses) {
		t.Fatalf("addresses = %v, want host addresses and prefixes %v", addresses, endpoint.Addresses)
	}
}

func TestHandleMessageWithoutTunnels(t *testing.T) {
	dir := t.TempDir()
	manager, err := network.LoadManager(filepath.Join(dir, "config"), filepath.Join(dir, "state"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name string
		msg  proto.PluginMessage
		want string
	}{
		{"create preserves JSON", proto.PluginMessage{Args: []string{"plugin", "create"}, Raw: []byte(`{"name":"ipv6","ipv6_enabled":true}`)}, `{"name":"ipv6","ipv6_enabled":true}`},
		{"teardown", proto.PluginMessage{Args: []string{"plugin", "teardown", "/missing/netns"}, Raw: []byte(`{"container_name":"absent"}`)}, ""},
		{"repeated teardown", proto.PluginMessage{Args: []string{"plugin", "teardown", "/missing/netns"}, Raw: []byte(`{"container_name":"absent"}`)}, ""},
	} {
		t.Run(tc.name, func(t *testing.T) {
			server, client := net.Pipe()
			defer client.Close()
			_ = client.SetDeadline(time.Now().Add(5 * time.Second))
			go handleMessage(manager, server, tc.msg)
			output, err := io.ReadAll(client)
			if err != nil {
				t.Fatal(err)
			}
			if string(output) != tc.want {
				t.Fatalf("response = %q, want %q", output, tc.want)
			}
		})
	}
}
