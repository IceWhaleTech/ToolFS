#!/usr/bin/env bash
# Build the current checkout's isolated Linux permission tests. No service is installed.
set -euo pipefail
ROOT=$(cd "$(dirname "$0")/.." && pwd)
OUT=''
REF=''
while [[ $# -gt 0 ]]; do
  case "$1" in
    --ref) REF=$2; shift 2 ;;
    --out) OUT=$2; shift 2 ;;
    *) echo "unknown argument: $1" >&2; exit 2 ;;
  esac
done
[[ -n "$OUT" ]] || { echo 'usage: build-artifacts.sh [--ref REF] --out DIR' >&2; exit 2; }
if [[ -n "$REF" && $(git -C "$ROOT" rev-parse "$REF^{commit}") != $(git -C "$ROOT" rev-parse HEAD) ]]; then
  echo 'the checked-out commit must match --ref' >&2
  exit 2
fi
mkdir -p "$OUT/tests"
OUT=$(cd "$OUT" && pwd)
cd "$ROOT"
CGO_ENABLED=0 GOOS=linux GOARCH=amd64 go test -c -trimpath -o "$OUT/tests/toolfs-agent-authorization.test" .
