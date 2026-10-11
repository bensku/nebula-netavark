#!/usr/bin/env bash
# Run against a local Linux host as root, after building the binaries as a user.
set -Eeuo pipefail

repo=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")/.." && pwd)
daemon="$repo/nebula-netavarkd"
plugin="$repo/nebula-netavark-plugin"
image=${E2E_IMAGE:-}
port_base=${E2E_PORT_BASE:-$((40000 + (RANDOM % 10000) * 2))}

usage() {
    cat <<'EOF'
Usage: sudo bash tests/e2e.sh [--daemon PATH] [--plugin PATH] [--image IMAGE] [--port-base PORT]
Build both binaries first (go tool task build). Requires local, rootful Podman,
Netavark, Podman's init helper (usually catatonit), nebula-cert with v2 support,
jq, ip, nsenter, timeout, and /dev/net/tun.
E2E_IMAGE and E2E_PORT_BASE supply defaults for the corresponding options.
Without --image, builds Alpine 3.22 with busybox-extras for the HTTP server.
EOF
}

while (($#)); do
    case "$1" in
        --help|-h) usage; exit 0 ;;
        --daemon|--plugin|--image|--port-base)
            (($# >= 2)) || { usage >&2; exit 2; }
            case "$1" in
                --daemon) daemon=$2 ;;
                --plugin) plugin=$2 ;;
                --image) image=$2 ;;
                --port-base) port_base=$2 ;;
            esac
            shift 2 ;;
        *) usage >&2; exit 2 ;;
    esac
done

fail() { printf 'FAIL: %s\n' "$*" >&2; exit 1; }
log() { printf '\n%s\n' "$*"; }

((EUID == 0)) || fail 'Run this harness with sudo; build the binaries as your normal user first.'
[[ $(uname -s) == Linux ]] || fail 'Linux is required.'
for tool in podman nebula-cert jq ip nsenter timeout; do
    command -v "$tool" >/dev/null || fail "Missing prerequisite: $tool"
done
[[ -c /dev/net/tun && -r /dev/net/tun && -w /dev/net/tun ]] || fail 'A readable/writable /dev/net/tun character device is required.'
[[ -x "$daemon" && -x "$plugin" ]] || fail 'Build both binaries first: go tool task build'
[[ $port_base =~ ^[0-9]{1,5}$ ]] || fail 'UDP port base must be an integer between 1024 and 65532.'
port_base=$((10#$port_base))
((port_base >= 1024 && port_base <= 65532)) || fail 'UDP port base must be between 1024 and 65532 (four ports are used, including the retry and host scenarios).'
[[ $(cat /proc/sys/net/ipv6/conf/lo/disable_ipv6) == 0 ]] || fail 'IPv6 must be enabled on the host loopback interface.'
ip -6 -o addr show dev lo | jq -Rse 'contains("::1/128")' >/dev/null || fail 'Host loopback must have ::1/128.'
cert_help=$(nebula-cert sign -help 2>&1 || true)
[[ $cert_help == *-networks* && $cert_help == *-version* ]] || fail 'nebula-cert must support v2 certificates (-networks and -version).'

umask 077
work=$(mktemp -d /tmp/nn-e2e.XXXXXXXX)
daemon_pid=
success=0
network_created=0
containers=()
network="nn-e2e-${work##*.}"
name_a="$network-a"
name_b="$network-b"
addr_a=fd42:6e62::2
addr_b=fd42:6e62::3
addr_host=fd42:6e62::1
host_dev="nn-${work##*.}"
mkdir -p "$work"/{bin,plugins,config,state,certs,storage,runroot,networks,tmp,logs}
cp -- "$daemon" "$work/bin/nebula-netavarkd"
cp -- "$plugin" "$work/bin/nebula-netavark-plugin"
daemon="$work/bin/nebula-netavarkd"
plugin="$work/bin/nebula-netavark-plugin"
socket="$work/control.sock"

cat >"$work/containers.conf" <<EOF
[network]
network_backend = "netavark"
netavark_plugin_dirs = ["$work/plugins"]
EOF
export CONTAINERS_CONF_OVERRIDE="$work/containers.conf"
# Do not inherit a custom storage configuration or a remote Podman connection.
unset CONTAINERS_STORAGE_CONF CONTAINER_HOST CONTAINER_CONNECTION
podman_args=(--remote=false --root "$work/storage" --runroot "$work/runroot"
    --storage-driver vfs --network-config-dir "$work/networks" --tmpdir "$work/tmp"
    --events-backend none)

pm() {
    printf '%q ' podman "${podman_args[@]}" "$@" >>"$work/logs/commands.log"
    printf '\n' >>"$work/logs/commands.log"
    timeout --foreground 45s podman "${podman_args[@]}" "$@"
}

daemon_ready() {
    [[ -n $daemon_pid ]] && kill -0 "$daemon_pid" 2>/dev/null &&
        NEBULA_CONTROL_SOCKET="$socket" timeout 3s "$plugin" info </dev/null 2>/dev/null |
        jq -e '.api_version == "1.0.0"' >/dev/null
}

wait_until() {
    local deadline=$((SECONDS + 20))
    until "$@" >>"$work/logs/retries.log" 2>&1; do
        ((SECONDS < deadline)) || return 1
        sleep 0.2
    done
}

start_daemon() {
    "$daemon" --socket "$socket" --config "$work/config" --state "$work/state" \
        >>"$work/logs/daemon.log" 2>&1 &
    daemon_pid=$!
    wait_until daemon_ready || fail "Daemon did not become ready; check $work/logs/daemon.log (UDP ports $port_base–$((port_base + 3)))."
}

stop_daemon() {
    local signal=$1
    kill -"$signal" "$daemon_pid"
    wait_until process_stopped "$daemon_pid" || fail 'Daemon did not stop.'
    wait "$daemon_pid" 2>/dev/null || true
    daemon_pid=
}

process_stopped() { ! kill -0 "$1" 2>/dev/null; }

cleanup() {
    local rc=$?
    trap - EXIT INT TERM
    set +e
    # Bring the service back if a failed outage scenario left it down, so Podman
    # can still call teardown for the test containers.
    if ((${#containers[@]})) && ! daemon_ready; then
        if [[ -n $daemon_pid ]]; then
            kill -TERM "$daemon_pid" 2>/dev/null
            if ! wait_until process_stopped "$daemon_pid"; then
                kill -KILL "$daemon_pid" 2>/dev/null
            fi
            wait "$daemon_pid" 2>/dev/null
        fi
        "$daemon" --socket "$socket" --config "$work/config" --state "$work/state" \
            >>"$work/logs/daemon.log" 2>&1 &
        daemon_pid=$!
        wait_until daemon_ready
    fi
    if ((${#containers[@]})); then
        pm inspect "${containers[@]}" >"$work/logs/cleanup-inspect.json" 2>"$work/logs/cleanup-inspect-errors.log"
        for container in "${containers[@]}"; do
            pm logs "$container" >"$work/logs/$container-container.log" 2>&1
        done
        pm rm --force --all >>"$work/logs/cleanup.log" 2>&1
        if [[ $? != 0 ]]; then rc=1; success=0; fi
    fi
    if ((network_created)); then
        pm network rm "$network" >>"$work/logs/cleanup.log" 2>&1
        if [[ $? != 0 ]]; then rc=1; success=0; fi
    fi
    if [[ -n $daemon_pid ]]; then
        kill -TERM "$daemon_pid" 2>/dev/null
        if ! wait_until process_stopped "$daemon_pid"; then
            kill -KILL "$daemon_pid" 2>/dev/null
            rc=1
            success=0
        fi
        wait "$daemon_pid" 2>/dev/null
    fi
    if ((success && rc == 0)); then
        rm -rf -- "$work"
        printf '\nPASS: IPv6 Podman end-to-end tests\n'
    else
        printf '\nTest artifacts retained at %s\n' "$work" >&2
        printf 'Inspect daemon.log, commands.log, retries.log, and cleanup.log under logs/.\n' >&2
    fi
    exit "$rc"
}
trap cleanup EXIT
trap 'exit 130' INT
trap 'exit 143' TERM

# The wrapper sets the socket even if Podman/Netavark filters its environment.
{
    printf '#!/usr/bin/env bash\n'
    printf 'export NEBULA_CONTROL_SOCKET=%q\n' "$socket"
    printf 'exec %q "$@"\n' "$plugin"
} >"$work/plugins/nebula-netavark-plugin"
chmod 700 "$work/plugins/nebula-netavark-plugin"

log "Generating IPv6 certificates (artifacts: $work)"
nebula-cert ca -name "$network" -version 2 -duration 1h \
    -out-crt "$work/certs/ca.crt" -out-key "$work/certs/ca.key"
cat >"$work/config/network.yml" <<EOF
pki:
  ca: "$work/certs/ca.crt"
  initiating_version: 2
lighthouse:
  am_lighthouse: false
  hosts: []
listen:
  host: "::1"
static_host_map:
  "$addr_host": ["[::1]:$((port_base + 3))"]
tun:
  dev: nebula-test
firewall:
  inbound:
    - {port: any, proto: any, host: any}
  outbound:
    - {port: any, proto: any, host: any}
EOF
write_endpoint() {
    local name=$1 address=$2 peer=$3 port=$4 peer_port=$5
    nebula-cert sign -name "$name" -version 2 -networks "$address/64" -duration 30m \
        -ca-crt "$work/certs/ca.crt" -ca-key "$work/certs/ca.key" \
        -out-crt "$work/certs/$name.crt" -out-key "$work/certs/$name.key"
    cat >"$work/config/$name.yml" <<EOF
import: network.yml
pki:
  cert: "$work/certs/$name.crt"
  key: "$work/certs/$name.key"
static_host_map:
  "$peer": ["[::1]:$peer_port"]
listen:
  port: $port
EOF
}
write_endpoint "$name_a" "$addr_a" "$addr_b" "$port_base" "$((port_base + 1))"
write_endpoint "$name_b" "$addr_b" "$addr_a" "$((port_base + 1))" "$port_base"
write_endpoint "$network-retry" fd42:6e62::4 "$addr_b" "$((port_base + 2))" "$((port_base + 1))"
# Host endpoint only answers; containers find it through the shared static_host_map
nebula-cert sign -name "$network-host" -version 2 -networks "$addr_host/64" -duration 30m \
    -ca-crt "$work/certs/ca.crt" -ca-key "$work/certs/ca.key" \
    -out-crt "$work/certs/$network-host.crt" -out-key "$work/certs/$network-host.key"
cat >"$work/config/_host.yml" <<EOF
import: network.yml
pki:
  cert: "$work/certs/$network-host.crt"
  key: "$work/certs/$network-host.key"
static_host_map: null
listen:
  port: $((port_base + 3))
tun:
  dev: "$host_dev"
EOF
start_daemon

log 'Checking plugin discovery and creating the IPv6 network'
pm info --format json >"$work/logs/podman-info.json"
jq -e '.host.networkBackend == "netavark" and (.plugins.network | index("nebula-netavark-plugin") != null)' \
    "$work/logs/podman-info.json" >/dev/null || fail 'Podman did not discover the plugin with the Netavark backend.'
# With IPAM disabled Podman forbids subnets. Nebula's certificates supply the
# addresses and prefixes; --ipv6 keeps IPv6 enabled in the network metadata.
pm network create --driver nebula-netavark-plugin --ipv6 \
    --ipam-driver none --disable-dns "$network"
network_created=1
pm network inspect "$network" >"$work/logs/network.json"
jq -e '.[0] | .ipv6_enabled == true and .driver == "nebula-netavark-plugin" and .ipam_options.driver == "none" and ((.subnets // []) | length) == 0' \
    "$work/logs/network.json" >/dev/null
if [[ -z $image ]]; then
    image="localhost/${network,,}:latest"
    log 'Building test image: Alpine 3.22 with busybox-extras'
    timeout --foreground 180s podman "${podman_args[@]}" build --network host --tag "$image" \
        --file "$repo/tests/Containerfile" "$repo/tests" >>"$work/logs/image.log" 2>&1 || \
        fail "Could not build the test image; check $work/logs/image.log."
else
    log "Preparing test image: $image"
    timeout --foreground 180s podman "${podman_args[@]}" pull "$image" >>"$work/logs/image.log" 2>&1 || \
        fail "Could not pull the test image; check $work/logs/image.log."
fi
pm run --rm --init --network none --entrypoint sh "$image" -ec \
    'command -v ping; command -v wget; command -v httpd' >>"$work/logs/image.log" 2>&1 || \
    fail "Image/init preflight failed; requires ping, wget, httpd, and Podman's init helper (usually catatonit). Check $work/logs/image.log."
# The init process forwards SIGTERM so sleep/httpd do not run as PID 1 and
# force Podman to use SIGKILL when stopping the test containers.
run_args=(--init --network "$network" --cap-add NET_RAW --security-opt label=disable
    --sysctl net.ipv6.conf.all.disable_ipv6=0 --sysctl net.ipv6.conf.default.disable_ipv6=0)

log 'Starting containers with only the Nebula network'
containers+=("$name_a")
pm run -d --name "$name_a" "${run_args[@]}" "$image" sleep 1d
containers+=("$name_b")
pm run -d --name "$name_b" "${run_args[@]}" "$image" sh -c \
    'mkdir -p /www; printf "nebula-ipv6-ok\n" > /www/index.html; exec httpd -f -p "[::]:8080" -h /www'

container_pid() { pm inspect "$1" | jq -er '.[0].State.Pid | select(. > 0)'; }
check_metadata() {
    local name=$1 address=$2 pid
    if ! pid=$(container_pid "$name"); then
        printf 'Container %s is not running:\n' "$name" >&2
        pm inspect "$name" | jq '.[0].State' >&2
        pm logs "$name" >&2
        return 1
    fi
    nsenter -t "$pid" -n ip -j -6 addr show dev nebula-test >"$work/logs/$name-addresses.json" || return 1
    jq -e --arg ip "$address" '.[0].addr_info | any(.local == $ip and .prefixlen == 64 and (.tentative != true))' \
        "$work/logs/$name-addresses.json" >/dev/null || { printf 'IPv6 address mismatch for %s\n' "$name" >&2; return 1; }
    nsenter -t "$pid" -n ip -j -6 route show >"$work/logs/$name-routes.json" || return 1
    jq -e 'any(.dev == "nebula-test" and .dst == "fd42:6e62::/64")' \
        "$work/logs/$name-routes.json" >/dev/null || { printf 'IPv6 route mismatch for %s\n' "$name" >&2; return 1; }
    pm inspect "$name" >"$work/logs/$name-inspect.json" || return 1
    jq -e --arg ip "$address" --arg network "$network" \
        '.[0].NetworkSettings.Networks[$network] | .GlobalIPv6Address == $ip and .GlobalIPv6PrefixLen == 64 and (.MacAddress // "") == ""' \
        "$work/logs/$name-inspect.json" >/dev/null || { printf 'Podman IPv6 inspect mismatch for %s\n' "$name" >&2; return 1; }
    [[ -s "$work/state/$name" ]]
}
check_host() {
    ip -j -6 addr show dev "$host_dev" >"$work/logs/host-addresses.json" &&
        jq -e --arg ip "$addr_host" '.[0].addr_info | any(.local == $ip and .prefixlen == 64)' \
            "$work/logs/host-addresses.json" >/dev/null
}
check_traffic() {
    pm exec "$name_a" ping -6 -c 1 -W 2 "$addr_b" &&
        pm exec "$name_a" ping -6 -c 1 -W 2 "$addr_host" &&
        pm exec "$name_b" ping -6 -c 1 -W 2 "$addr_a" &&
        [[ $(pm exec "$name_a" wget -Y off -q -T 3 -O - "http://[$addr_b]:8080/") == nebula-ipv6-ok ]]
}
check_endpoints() {
    if ! wait_until check_host; then
        tail -n 40 "$work/logs/retries.log" >&2
        fail "Host endpoint $host_dev is missing or has wrong address."
    fi
    if ! wait_until check_metadata "$name_a" "$addr_a"; then
        tail -n 40 "$work/logs/retries.log" >&2
        fail "IPv6 metadata mismatch for $name_a."
    fi
    if ! wait_until check_metadata "$name_b" "$addr_b"; then
        tail -n 40 "$work/logs/retries.log" >&2
        fail "IPv6 metadata mismatch for $name_b."
    fi
    if ! wait_until check_traffic; then
        tail -n 40 "$work/logs/retries.log" >&2
        fail 'IPv6 ping/HTTP did not recover.'
    fi
}
check_endpoints

log 'Checking container stop/start and endpoint persistence'
pm stop --time 2 "$name_a"
[[ ! -e "$work/state/$name_a" ]] || fail 'Stopping the container left its endpoint state behind.'
[[ -s "$work/state/$name_b" ]] || fail 'Stopping one container removed the other endpoint.'
pm start "$name_a"
check_endpoints

for signal in TERM KILL; do
    log "Checking daemon recovery after SIG$signal"
    pid_a=$(container_pid "$name_a")
    pid_b=$(container_pid "$name_b")
    if [[ $signal == TERM ]]; then
        cp "$work/config/$name_a.yml" "$work/config/stale.yml"
        printf '%s' "$work/nonexistent-netns" >"$work/state/stale"
    fi
    stop_daemon "$signal"
    [[ -s "$work/state/$name_a" && -s "$work/state/$name_b" ]] || fail 'Daemon shutdown removed restart state.'
    start_daemon
    [[ ! -e "$work/state/stale" ]] || fail 'Recovery did not remove a stale namespace entry.'
    check_endpoints
    [[ $(container_pid "$name_a") == "$pid_a" && $(container_pid "$name_b") == "$pid_b" ]] || fail 'Daemon recovery restarted a container.'
done

expect_start_failure() {
    local name=$1 expected=$2 output rc
    containers+=("$name")
    set +e
    output=$(pm run -d --name "$name" "${run_args[@]}" "$image" sleep 1d 2>&1)
    rc=$?
    set -e
    printf '%s\n' "$output" >"$work/logs/$name-failure.log"
    ((rc != 0 && rc != 124 && rc != 137)) || fail "Expected an ordinary startup failure for $name; exit status $rc."
    [[ $output == *"$expected"* ]] || fail "Startup failure did not contain '$expected': $output"
    [[ ! -e "$work/state/$name" ]] || fail "Failed startup persisted an endpoint for $name."
}

log 'Checking missing and malformed configurations'
expect_start_failure "$network-missing" 'could not load config'
printf 'pki: [\n' >"$work/config/$network-malformed.yml"
expect_start_failure "$network-malformed" 'failed to load Nebula config'

log 'Checking daemon outage and successful retry'
stop_daemon TERM
expect_start_failure "$network-retry" 'dial unix'
start_daemon
pm start "$network-retry"
wait_until check_metadata "$network-retry" fd42:6e62::4 || fail 'Retry after daemon outage did not create an IPv6 endpoint.'
wait_until pm exec "$network-retry" ping -6 -c 1 -W 2 "$addr_b" || fail 'Retry container could not reach its IPv6 peer.'
pm stop --time 2 "$network-retry"
[[ ! -e "$work/state/$network-retry" ]] || fail 'Retry container teardown left endpoint state.'
check_endpoints

log 'Checking final teardown'
pm stop --time 2 "$name_a" "$name_b"
[[ ! -e "$work/state/$name_a" && ! -e "$work/state/$name_b" ]] || fail 'Final teardown left endpoint state.'
success=1
