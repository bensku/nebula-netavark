package network

import (
	"log/slog"
	"net/netip"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/slackhq/nebula/config"
)

func writeConfig(t *testing.T, dir, name, contents string) string {
	t.Helper()
	path := filepath.Join(dir, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
	return path
}

func load(t *testing.T, dir, name string) (*config.C, error) {
	t.Helper()
	c, _, _, err := loadConfig(dir, name, slog.Default())
	return c, err
}

func TestConfigImportOverrides(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "network.yml", `
pki: {ca: shared-ca.crt, cert: baseline.crt, key: baseline.key}
listen: {host: "::", port: 4242}
lighthouse: {am_lighthouse: true, hosts: [one, two]}
firewall:
  inbound: [{port: any, proto: any, host: any}]
  outbound: [{port: any, proto: any, host: any}]
optional: {enabled: true}
shape: scalar
replace_map: {nested: value}
`)
	path := "endpoints/container.yml"
	writeConfig(t, dir, path, `
import: network.yml
pki: {cert: container.crt, key: container.key}
listen: {port: 0}
lighthouse: {am_lighthouse: false, hosts: []}
firewall:
  inbound: [{port: 80, proto: tcp, host: any}]
optional: null
shape: {nested: value}
replace_map: scalar
`)
	c, err := load(t, dir, path)
	if err != nil {
		t.Fatal(err)
	}
	for key, want := range map[string]any{
		"pki.ca": "shared-ca.crt", "pki.cert": "container.crt", "pki.key": "container.key",
		"listen.host": "::", "listen.port": 0, "lighthouse.am_lighthouse": false,
		"lighthouse.hosts": []any{}, "optional": nil, "shape.nested": "value", "import": nil,
		"replace_map":       "scalar",
		"firewall.inbound":  []any{map[string]any{"port": 80, "proto": "tcp", "host": "any"}},
		"firewall.outbound": []any{map[string]any{"port": "any", "proto": "any", "host": "any"}},
	} {
		if got := c.Get(key); !reflect.DeepEqual(got, want) {
			t.Errorf("%s = %#v, want %#v", key, got, want)
		}
	}
}

func TestSharedBaselineEndpoints(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "network.yml", "pki: {ca: shared.crt, cert: baseline.crt}\n")
	writeConfig(t, dir, "first.yml", "import: network.yml\npki: {cert: first.crt}\n")
	writeConfig(t, dir, "second.yml", "import: network.yml\npki: {cert: second.crt}\n")
	first, err := load(t, dir, "first.yml")
	if err != nil {
		t.Fatal(err)
	}
	second, err := load(t, dir, "second.yml")
	if err != nil {
		t.Fatal(err)
	}
	if first.GetString("pki.cert", "") != "first.crt" || second.GetString("pki.cert", "") != "second.crt" {
		t.Fatal("endpoints did not keep their own certificates")
	}
	first.GetMap("pki", nil)["ca"] = "mutated.crt"
	if second.GetString("pki.ca", "") != "shared.crt" {
		t.Fatal("endpoint settings share mutable maps")
	}
}

func TestConfigSubdirectoryImports(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "shared/base.yml", "pki: {ca: shared.crt}\n")
	writeConfig(t, dir, "shared/site.yml", "import: shared/base.yml\nlisten: {port: 1}\n")
	writeConfig(t, dir, "endpoint.yml", "import: shared/site.yml\n")
	c, err := load(t, dir, "endpoint.yml")
	if err != nil {
		t.Fatal(err)
	}
	if c.GetString("pki.ca", "") != "shared.crt" || c.GetInt("listen.port", 0) != 1 {
		t.Fatalf("unexpected merged settings: %#v", c.Settings)
	}
}

func TestConfigNestedAndOrderedImports(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "base.yml", "pki: {ca: shared.crt}\nlisten: {host: localhost, port: 1}\n")
	writeConfig(t, dir, "first.yml", "import: base.yml\nlisten: {port: 2}\n")
	writeConfig(t, dir, "second.yml", "import: base.yml\nlisten: {port: 3}\n")
	writeConfig(t, dir, "endpoint.yml", "import: [first.yml, second.yml]\npki: {cert: endpoint.crt}\n")
	c, err := load(t, dir, "endpoint.yml")
	if err != nil {
		t.Fatal(err)
	}
	if c.GetInt("listen.port", 0) != 3 || c.GetString("listen.host", "") != "localhost" || c.GetString("pki.ca", "") != "shared.crt" {
		t.Fatalf("unexpected merged settings: %#v", c.Settings)
	}
	// A new load reads changes to the baseline
	writeConfig(t, dir, "base.yml", "pki: {ca: changed.crt}\n")
	c, err = load(t, dir, "endpoint.yml")
	if err != nil {
		t.Fatal(err)
	}
	if c.GetString("pki.ca", "") != "changed.crt" {
		t.Fatalf("baseline changes were not loaded: %#v", c.Settings)
	}
}

func TestConfigImportErrors(t *testing.T) {
	for _, tc := range []struct{ name, yaml, want string }{
		{"missing", "import: absent.yml", "absent.yml"},
		{"self cycle", "import: endpoint.yml", "nested too deeply"},
		{"nested cycle", "import: nested.yml", "nested too deeply"},
		{"malformed baseline", "import: malformed.yml", "malformed.yml"},
		{"null", "import: null", "import must be"},
		{"empty path", "import: ''", "inside config directory"},
		{"absolute", "import: /etc/base.yml", "inside config directory"},
		{"parent", "import: ../base.yml", "inside config directory"},
		{"escaping subdirectory", "import: sub/../../base.yml", "inside config directory"},
		{"number", "import: 123", "import must be"},
		{"map", "import: {file: baseline.yml}", "import must be"},
		{"invalid list", "import: [baseline.yml, false]", "import must be"},
		{"empty list item", "import: ['']", "inside config directory"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := t.TempDir()
			writeConfig(t, dir, "endpoint.yml", tc.yaml)
			writeConfig(t, dir, "nested.yml", "import: endpoint.yml")
			writeConfig(t, dir, "malformed.yml", "pki: [")
			_, err := load(t, dir, "endpoint.yml")
			if err == nil || !strings.Contains(err.Error(), tc.want) || !strings.Contains(err.Error(), "endpoint.yml") {
				t.Fatalf("error = %v, want endpoint context and %q", err, tc.want)
			}
		})
	}
}

func TestConfigWithoutImports(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "plain.yml", "listen: {port: 4242}\n")
	writeConfig(t, dir, "empty-imports.yml", "import: []\nlisten: {port: 4242}\n")
	for _, name := range []string{"plain.yml", "empty-imports.yml"} {
		c, err := load(t, dir, name)
		if err != nil || c.GetInt("listen.port", 0) != 4242 || c.Get("import") != nil {
			t.Fatalf("%s: config = %v, error = %v", name, c, err)
		}
	}
}

func TestConfigRawDetectsChanges(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "base.yml", "listen: {port: 1}\n")
	writeConfig(t, dir, "endpoint.yml", "import: base.yml\n")
	_, before, _, err := loadConfig(dir, "endpoint.yml", slog.Default())
	if err != nil {
		t.Fatal(err)
	}
	writeConfig(t, dir, "base.yml", "# Only a comment changed\nlisten:   {port: 1}\n")
	_, same, _, _ := loadConfig(dir, "endpoint.yml", slog.Default())
	writeConfig(t, dir, "base.yml", "listen: {port: 2}\n")
	_, changed, _, _ := loadConfig(dir, "endpoint.yml", slog.Default())
	if before != same || before == changed {
		t.Fatalf("raw configs: before %q, cosmetic edit %q, real edit %q", before, same, changed)
	}
}

func TestUnsafeAddresses(t *testing.T) {
	dir := t.TempDir()
	writeConfig(t, dir, "network.yml", "unsafe_addresses: [\"fd42:7669::1\"]\nlisten: {port: 1}\n")
	writeConfig(t, dir, "inherit.yml", "import: network.yml\n")
	writeConfig(t, dir, "override.yml", `
import: network.yml
unsafe_addresses: ["fd42:7669::2/128", "10.99.0.1", "10.99.0.2/32", "::ffff:10.99.0.3", "FD42:7669::2"]
`)
	writeConfig(t, dir, "clear.yml", "import: network.yml\nunsafe_addresses: null\n")
	for name, want := range map[string][]netip.Prefix{
		"inherit.yml": {netip.MustParsePrefix("fd42:7669::1/128")},
		"override.yml": {netip.MustParsePrefix("fd42:7669::2/128"), netip.MustParsePrefix("10.99.0.1/32"),
			netip.MustParsePrefix("10.99.0.2/32"), netip.MustParsePrefix("10.99.0.3/32")},
		"clear.yml": nil,
	} {
		c, raw, addrs, err := loadConfig(dir, name, slog.Default())
		if err != nil {
			t.Fatalf("%s: %v", name, err)
		}
		if !reflect.DeepEqual(addrs, want) {
			t.Errorf("%s: addresses = %v, want %v", name, addrs, want)
		}
		// Nebula must not see our key, and changing only it must not reload Nebula
		if c.Get(unsafeAddressesKey) != nil || strings.Contains(raw, unsafeAddressesKey) {
			t.Errorf("%s: %s was passed to Nebula", name, unsafeAddressesKey)
		}
		if c.GetInt("listen.port", 0) != 1 {
			t.Errorf("%s: rest of config was lost", name)
		}
	}
}

func TestInvalidUnsafeAddresses(t *testing.T) {
	for _, value := range []string{
		`"fd42:7669::1"`, `[fd42:7669::/64]`, `[10.99.0.0/24]`, `[not-an-address]`, `[1]`,
		`[{address: "fd42:7669::1"}]`, `["fe80::1%lo"]`,
	} {
		dir := t.TempDir()
		writeConfig(t, dir, "endpoint.yml", "unsafe_addresses: "+value+"\n")
		if _, _, _, err := loadConfig(dir, "endpoint.yml", slog.Default()); err == nil {
			t.Errorf("unsafe_addresses %s was accepted", value)
		} else if !strings.Contains(err.Error(), "config endpoint.yml") {
			t.Errorf("unsafe_addresses %s: error %q does not name config", value, err)
		}
	}
}
