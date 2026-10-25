#!/usr/bin/env bash
set -euo pipefail

dist=$(realpath "${1:-dist}")
test_dir=$(mktemp -d)
server_pid=
cleanup() {
  result=$?
  if [[ -n "$server_pid" ]]; then
    kill "$server_pid" 2>/dev/null || true
    wait "$server_pid" 2>/dev/null || true
  fi
  if [[ "$result" -ne 0 && -f "$test_dir/server.log" ]]; then
    cat "$test_dir/server.log"
  fi
  rm -rf "$test_dir"
}
trap cleanup EXIT

(cd "$dist" && sha256sum --check checksums.txt)

for arch in amd64 arm64; do
  packages=("$dist"/linux-"$arch"-toolfsd-v*.tar.gz)
  [[ ${#packages[@]} -eq 1 && -f "${packages[0]}" ]]
  mkdir -p "$test_dir/$arch"
  tar -xzf "${packages[0]}" -C "$test_dir/$arch"
  sysroot="$test_dir/$arch/build/sysroot"
  binary="$sysroot/usr/bin/toolfsd"
  test -x "$binary"
  test -s "$sysroot/usr/lib/systemd/system/toolfsd.service"
  test -s "$sysroot/usr/lib/modules-load.d/toolfsd.conf"
  test -s "$sysroot/etc/casaos/toolfsd.env.sample"
  test ! -e "$sysroot/etc/casaos/toolfsd.env"
  python3 - "$binary" "$arch" <<'PY'
import sys
with open(sys.argv[1], "rb") as binary:
    header = binary.read(64)
assert header[:6] == b"\x7fELF\x02\x01", "Expected a little-endian ELF64 binary"
assert int.from_bytes(header[18:20], "little") == {"amd64": 62, "arm64": 183}[sys.argv[2]], "Wrong target architecture"
PY
  if readelf -l "$binary" | grep -q INTERP; then
    echo "toolfsd must not depend on a dynamic ELF interpreter" >&2
    exit 1
  fi
done

case "$(uname -m)" in
  x86_64) native_arch=amd64 ;;
  aarch64|arm64) native_arch=arm64 ;;
  *) echo "No native daemon smoke test for this host architecture" >&2; exit 1 ;;
esac
binary="$test_dir/$native_arch/build/sysroot/usr/bin/toolfsd"
"$binary" -h > "$test_dir/help.txt" 2>&1
test -c /dev/fuse
mkdir -p "$test_dir/runtime"
socket="$test_dir/toolfsd.sock"
"$binary" -root "$test_dir/mounts" -socket "$socket" \
  -runtime-path "$test_dir/runtime" -permission-cache "" \
  > "$test_dir/server.log" 2>&1 &
server_pid=$!
ready=false
for ((attempt = 0; attempt < 50; attempt++)); do
  if curl --fail --silent --max-time 2 --unix-socket "$socket" http://localhost/healthz > "$test_dir/health.json"; then
    ready=true
    break
  fi
  kill -0 "$server_pid"
  sleep 0.1
done
[[ "$ready" == true ]]
python3 - "$test_dir/health.json" <<'PY'
import json
import sys
with open(sys.argv[1]) as response:
    health = json.load(response)
assert health["status"] == "ok" and health["mounts"] == 0, health
PY
[[ "$(stat -c %a "$socket")" == 600 ]]
status=$(curl --silent --show-error --max-time 2 --unix-socket "$socket" \
  -o "$test_dir/refused.json" -w '%{http_code}' \
  -H 'Content-Type: application/json' -d '{"agent_user_id":"unbound-smoke-test"}' \
  http://localhost/v1/mounts)
[[ "$status" == 403 ]]
kill "$server_pid"
wait "$server_pid"
server_pid=
test ! -e "$socket"
echo "toolfsd packages passed checksums, architecture, static linking, native startup, control API, and shutdown checks."
