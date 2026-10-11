# nebula-netavark
[Nebula](https://github.com/slackhq/nebula) overlay networks for Podman.

## Installation
Install the binaries:
* `nebula-netavarkd` to `/usr/bin` (or wherever you want to, just edit the systemd unit)
* `nebula-netavark-plugin` to `/usr/lib/netavark/nebula` (Podman will find it there)

Next, install the [nebula-netavarkd.service](./systemd/nebula-netavarkd.service) to
`/etc/systemd/system` and enable it:
```sh
systemctl enable --now nebula-netavarkd
systemctl status nebula-netavarkd
```
The service should not, without any configuration, do anything.

For container networking, you'll need to create Podman network with driver `nebula`.
For clarity, it is best if the network's name is also `nebula`, though this is not required.
Creating a network can be done with e.g. a quadlet network unit,
see [nebula.network](./systemd/nebula.network).

## Usage
One container can currently have at most one Nebula endpoint. The endpoint
configuration is located using the container's name.

Put the Nebula configuration in `/etc/containers/nebula/${CONTAINER_NAME}.yml`.
Format is same as as Nebula's [config.yml](https://github.com/slackhq/nebula/blob/master/examples/config.yml),
with a few additions outlined below.
Once the configuration is in place, simply start the container with
`--network=nebula` and its network namespace should receive Nebula TUN device.

Note that by default, Nebula will not grant your containers Internet access. If
you want that, you can also join them in Podman bridge (or, if you're feeling
adventurous and have publicly routable IPs, unsafe route straight to Internet).

Unlike vanilla Nebula, **nebula-netavarkd automatically reloads configurations**.
No need to send SIGHUP. You might want to keep key material embedded in
configurations, because changes to those will not trigger reloads.

### Host endpoint
To enable Nebula connectivity on host, create `/etc/containers/nebula/_host.yml`.
This gets you a normal Nebula endpoint on host, so that you need not to run
vanilla Nebula at all.

Also, you'll be able to use below additional features!

## nebula-netavark additions
We support some custom things in config.

### Imports
Config files can import each other. This may be useful if you have many
containers in same network:

First, define what you want to import:
```sh
# /etc/containers/nebula/networks/prod.yml
pki:
  ca: /etc/nebula/ca.crt
# lighthouses, etc.
```

```sh
import: networks/prod.yml
pki:
  cert: /etc/nebula/my-container.crt
  key: /etc/nebula/my-container.key
# ... rest of endpoint configuration
```

Or perhaps you might find this useful for e.g. shared firewall rules.
It just merges files!

## Unsafe addresses
If you're using `unsafe_routes` for virtual IPs, nebula-netavarkd can create
them for you:

```yaml
# backend-1.yml and backend-2.yml
import: network.yml
unsafe_addresses: ["fd42:7669::1"]
```

```yaml
# On clients
tun:
  unsafe_routes:
    - route: fd42:7669::1/128
      install: true
      via:
        - gateway: "fd42:6e62::3" # backend-1, primary
          weight: 1000000
        - gateway: "fd42:6e62::5" # backend-2, backup
          weight: 1
```

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
