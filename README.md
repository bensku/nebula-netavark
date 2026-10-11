# nebula-netavark
[Nebula](https://github.com/slackhq/nebula) overlay networks for Podman.
Usage instructions TBD!

## Shared endpoint configuration

The daemon loads `<container-name>.yml` from its `--config` directory
(default: `/etc/containers/nebula`). An endpoint file can import a shared baseline
and override only the values that differ for that container.

For example, `network.yml` can contain:

```yaml
pki:
  ca: /etc/nebula/ca.crt
lighthouse:
  hosts: [192.168.100.1]
static_host_map:
  "192.168.100.1": ["203.0.113.10:4242"]
listen:
  host: 0.0.0.0
  port: 0
firewall:
  inbound:
    - {port: any, proto: any, host: any}
  outbound:
    - {port: any, proto: any, host: any}
```

Then `my-container.yml` only needs:

```yaml
import: network.yml
pki:
  cert: /etc/nebula/my-container.crt
  key: /etc/nebula/my-container.key
```

`import` accepts a path or a list, such as `import: [network.yml, sites/hel.yml]`.
Import paths are always relative to the config directory, even in files that
are themselves in a subdirectory. They must stay inside it; absolute paths and
`..` are rejected. Imported files can import other files, up to 8 levels deep.
Imports are applied in list order, with later imports overriding earlier ones,
and the importing file's own settings overriding all imports. Missing files,
malformed YAML, invalid import values and cycles fail endpoint startup.

Nested maps merge recursively. Scalars and lists replace baseline values;
lists are **not appended**, including firewall rules. Explicit `false`, `0`,
`[]`, and `null` override baseline values too. An empty map preserves inherited
map entries; use `null` to clear a whole inherited section. The `import` key is
removed before Nebula receives the settings. Paths in Nebula settings, such as
`pki.cert`, retain Nebula's existing path behavior.

### Reloading

The daemon watches the config directory and its subdirectories. After any file
in them changes, every endpoint's config is loaded again, and endpoints whose
effective config changed are reloaded. If an edit leaves a config invalid, the
error is logged and the endpoint keeps running with its previous config until
the file is fixed.

Symlinked subdirectories are not supported, and changes to symlink targets
outside the config directory are not detected.

## Host endpoint

If `_host.yml` exists in the config directory, the daemon also runs Nebula for
the host itself, with its interface in the host network namespace. This can
replace a separate Nebula service. Container names can't start with an
underscore, so this can't clash with container configs.

`_host.yml` supports imports and reloading like any other config. Every endpoint
shares the host's UDP stack, so give the host endpoint its own `listen.port`
(e.g. 4242) and keep containers on `port: 0`.

If the host endpoint fails to start, the error is logged and containers keep
working; startup is retried when config changes. Removing `_host.yml` does not
stop a running host endpoint; restart the daemon for that.

Note that restarting or upgrading the daemon also briefly takes down the host's
Nebula connection, not just containers'.

## Testing
Run unit/regression tests with:
```sh
go test ./...
```

There are also E2E tests that actually use Podman. You'll need:
* Linux machine with sudo/root
* Podman
* nebula-cert

To run the tests, do:
```sh

go tool task test:e2e
```
If you are not already root, this will ask for sudo.

There are a bunch of env variables and CLI arguments you might pass,
see `tests/e2e.sh` for details.
