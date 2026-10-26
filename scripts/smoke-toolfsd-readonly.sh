#!/usr/bin/env bash
set -euo pipefail

dist=$(realpath "${1:-dist}")
script_dir=$(CDPATH='' cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
readonly_mnt=$(mktemp -d)
trap 'rmdir "$readonly_mnt"' EXIT

# Isolate the mounts: the host's /mnt and /run are left untouched.
# An empty read-only /mnt reproduces the ZimaOS squashfs failure even if a
# previous test created /mnt/toolfs. /run models systemd's writable runtime dir.
unshare --mount --propagation private bash -euo pipefail -c '
  mount --bind "$1" /mnt
  mount -o remount,bind,ro /mnt
  mount -t tmpfs -o mode=0755 tmpfs /run
  install -d -m 0700 /run/toolfs
  export TOOLFSD_TEST_DEFAULT_ROOT=1
  exec bash "$2" "$3"
' bash "$readonly_mnt" "$script_dir/smoke-toolfsd.sh" "$dist"
