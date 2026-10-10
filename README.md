# nebula-netavark
[Nebula](https://github.com/slackhq/nebula) overlay networks for Podman.
Usage instructions TBD!

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