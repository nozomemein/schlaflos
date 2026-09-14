#!/bin/bash
# Runs the privileged integration test inside a throwaway macOS VM managed by
# Tart (https://tart.run). Nothing on the host is modified.
#
# usage: scripts/integration-vm.sh [/path/to/schlaflos]
#
# environment:
#   TART_IMAGE  OCI image to clone (default ghcr.io/cirruslabs/macos-sequoia-vanilla:latest)
#   TART_VM     VM name (default schlaflos-integration)
#   KEEP_VM=1   keep the VM after the run instead of deleting it
#   VM_PASSWORD guest password (default admin, the Tart image default)
set -euo pipefail

HERE="$(cd "$(dirname "$0")" && pwd)"
ROOT="$(cd "$HERE/.." && pwd)"
BIN="${1:-$ROOT/bin/schlaflos}"
IMAGE="${TART_IMAGE:-ghcr.io/cirruslabs/macos-sequoia-vanilla:latest}"
VM="${TART_VM:-schlaflos-integration}"
USER_NAME=admin
PASSWORD="${VM_PASSWORD:-admin}"
EXPECT="$HERE/tart-expect.exp"
SSH_OPTS=(-o StrictHostKeyChecking=no -o UserKnownHostsFile=/dev/null -o LogLevel=ERROR)

command -v tart >/dev/null || { echo "tart is required: brew install cirruslabs/cli/tart" >&2; exit 2; }
[[ -x "$BIN" ]] || { echo "binary not found: $BIN (run: just build)" >&2; exit 2; }
if [[ "$(uname -m)" != "arm64" ]]; then echo "Tart macOS guests require Apple silicon" >&2; exit 2; fi

if ! tart list 2>/dev/null | awk '{print $2}' | grep -qx "$VM"; then
  echo "== cloning $IMAGE as $VM (first run downloads the image; this takes a while)"
  tart clone "$IMAGE" "$VM"
fi

echo "== starting $VM"
tart run --no-graphics "$VM" >/dev/null 2>&1 &
TART_PID=$!
cleanup() {
  echo "== stopping $VM"
  tart stop "$VM" >/dev/null 2>&1 || true
  wait "$TART_PID" 2>/dev/null || true
  if [[ "${KEEP_VM:-0}" != "1" ]]; then
    tart delete "$VM" >/dev/null 2>&1 || true
    echo "== deleted $VM (set KEEP_VM=1 to keep it)"
  fi
}
trap cleanup EXIT

IP=""
for _ in $(seq 1 120); do
  IP="$(tart ip "$VM" 2>/dev/null || true)"
  if [[ -n "$IP" ]] && nc -z -w 1 "$IP" 22 >/dev/null 2>&1; then break; fi
  IP=""
  sleep 2
done
[[ -n "$IP" ]] || { echo "VM did not become reachable over ssh" >&2; exit 1; }
echo "== VM reachable at $IP"

echo "== copying binary and test script"
"$EXPECT" "$PASSWORD" scp "${SSH_OPTS[@]}" "$BIN" "$HERE/integration-privileged.sh" "$USER_NAME@$IP:/tmp/"

echo "== running privileged integration test in the VM"
CODE=0
"$EXPECT" "$PASSWORD" ssh "${SSH_OPTS[@]}" "$USER_NAME@$IP" \
  "sudo SCHLAFLOS_INTEGRATION_CONFIRM=1 /bin/bash /tmp/integration-privileged.sh /tmp/schlaflos" || CODE=$?
echo "== integration test exit code: $CODE"
exit "$CODE"
