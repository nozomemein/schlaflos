#!/bin/bash
# Privileged integration test for schlaflos (delivery plan step 6).
#
# Run this ONLY on a dedicated Mac or a throwaway VM: it installs the
# LaunchDaemon, changes the global `pmset disablesleep` setting and the
# recurring wake schedule, and then removes everything again. It refuses to
# run unless SCHLAFLOS_INTEGRATION_CONFIRM=1 is set and it is running as root.
#
# usage: sudo SCHLAFLOS_INTEGRATION_CONFIRM=1 scripts/integration-privileged.sh /path/to/schlaflos
set -euo pipefail

BIN="${1:-}"
if [[ "${SCHLAFLOS_INTEGRATION_CONFIRM:-}" != "1" ]]; then
  echo "refusing to run: set SCHLAFLOS_INTEGRATION_CONFIRM=1 on a dedicated Mac or VM" >&2
  exit 2
fi
if [[ "$(id -u)" != "0" ]]; then
  echo "refusing to run: must be root" >&2
  exit 2
fi
if [[ -z "$BIN" || ! -x "$BIN" ]]; then
  echo "usage: $0 /path/to/schlaflos" >&2
  exit 2
fi
BIN="$(cd "$(dirname "$BIN")" && pwd)/$(basename "$BIN")"

LABEL=io.github.nozomemein.schlaflos
STATUS=/private/var/db/schlaflos/status.json
WORK="$(mktemp -d /private/tmp/schlaflos-it.XXXXXX)"
PASS=0
FAIL=0

log()  { printf '\n== %s\n' "$*"; }
ok()   { PASS=$((PASS + 1)); printf '  [ok]   %s\n' "$*"; }
fail() { FAIL=$((FAIL + 1)); printf '  [FAIL] %s\n' "$*"; }

assert_eq() { # actual expected description
  if [[ "$1" == "$2" ]]; then ok "$3 = $2"; else fail "$3 = $1, expected $2"; fi
}
assert_contains() { # haystack needle description
  if [[ "$1" == *"$2"* ]]; then ok "$3 contains $2"; else fail "$3 does not contain $2: $1"; fi
}
assert_exit() { # expected description command...
  local expected="$1" desc="$2"; shift 2
  local code=0
  "$@" >"$WORK/last.out" 2>&1 || code=$?
  if [[ "$code" == "$expected" ]]; then ok "$desc exits $expected"; else fail "$desc exits $code, expected $expected"; sed 's/^/         /' "$WORK/last.out"; fi
}

sleep_disabled() { /usr/bin/pmset -g | awk '$1 == "SleepDisabled" {print $2}' | grep . || echo 0; }
repeating()      { /usr/bin/pmset -g sched | awk '/Repeating power events:/{f=1;next} /Scheduled power events:/{f=0} f' | sed 's/^ *//' | tr '\n' ';'; }
loaded()         { /bin/launchctl print "system/$LABEL" >/dev/null 2>&1 && echo yes || echo no; }
status_field()   { grep -oE "\"$1\": *(\"[^\"]*\"|[a-z0-9]+)" "$STATUS" 2>/dev/null | head -1 | sed -E 's/^"[^"]*": *//; s/^"//; s/"$//' || true; }
wait_status() {  # field value
  local i
  for i in $(seq 1 60); do
    if [[ "$(status_field "$1")" == "$2" ]]; then return 0; fi
    sleep 1
  done
  return 1
}

PRE_SLEEP="$(sleep_disabled)"
PRE_REPEAT="$(repeating)"
log "pre-state: disablesleep=$PRE_SLEEP repeating='${PRE_REPEAT:-none}'"
REPLACE=()
if [[ -n "$PRE_REPEAT" ]]; then REPLACE=(--replace-wake-schedule); fi

cat >"$WORK/inside.toml" <<TOML
version = 1
poll_interval = "5s"
[power]
require_ac = true
[[windows]]
days = ["mon", "tue", "wed", "thu", "fri", "sat", "sun"]
start = "00:00"
end = "23:59"
[wake]
enabled = true
days = ["mon", "tue", "wed", "thu", "fri", "sat", "sun"]
time = "08:00"
action = "wakeorpoweron"
[[guards.process]]
name = "never-running"
executable = "/nonexistent/schlaflos-integration-guard"
TOML

cat >"$WORK/outside.toml" <<TOML
version = 1
poll_interval = "5s"
[power]
require_ac = true
TOML

cleanup() {
  if [[ -e /Library/LaunchDaemons/$LABEL.plist || -d "/Library/Application Support/schlaflos" ]]; then
    log "cleanup: uninstalling leftovers"
    "$BIN" uninstall || true
  fi
  rm -rf "$WORK"
}
trap cleanup EXIT

log "config check"
assert_exit 0 "config check inside"  "$BIN" config check "$WORK/inside.toml"
assert_exit 0 "config check outside" "$BIN" config check "$WORK/outside.toml"

log "install"
assert_exit 0 "install" "$BIN" install --config "$WORK/inside.toml" ${REPLACE[@]+"${REPLACE[@]}"}
assert_eq "$(loaded)" yes "launchd loaded"
assert_eq "$(stat -f '%Su:%Sg %Lp' '/Library/Application Support/schlaflos/config.toml')" "root:wheel 600" "config ownership/mode"
assert_eq "$(stat -f '%Su:%Sg %Lp' /private/var/db/schlaflos/state.json)" "root:wheel 600" "state ownership/mode"
assert_eq "$(stat -f '%Su:%Sg %Lp' "/Library/LaunchDaemons/$LABEL.plist")" "root:wheel 600" "plist ownership/mode"
if wait_status reason scheduled_window; then ok "first reconciliation reached scheduled_window"; else fail "status never reached scheduled_window: $(cat "$STATUS" 2>/dev/null)"; fi
assert_eq "$(sleep_disabled)" 1 "disablesleep inside window"
assert_contains "$(repeating)" "wakepoweron at 8:00AM every day" "repeating schedule"
assert_eq "$(stat -f '%Su:%Sg %Lp' "$STATUS")" "root:wheel 644" "status ownership/mode"
assert_exit 0 "status" "$BIN" status
assert_exit 0 "status --json" "$BIN" status --json
assert_exit 0 "doctor as root" "$BIN" doctor
assert_exit 0 "reconcile is idempotent" "$BIN" reconcile
assert_eq "$(status_field run_ok)" true "run_ok after manual reconcile"
assert_exit 0 "reconcile --dry-run" "$BIN" reconcile --dry-run

log "config apply (no windows, wake disabled)"
assert_exit 0 "config apply outside" "$BIN" config apply "$WORK/outside.toml"
if wait_status reason outside_window; then ok "reconciliation reached outside_window"; else fail "status never reached outside_window"; fi
assert_eq "$(sleep_disabled)" "$PRE_SLEEP" "disablesleep restored to baseline outside window"
assert_eq "$(repeating)" "$PRE_REPEAT" "repeating schedule handed back"

log "drift detection"
/usr/bin/pmset -a disablesleep 1
assert_exit 0 "reconcile after external change" "$BIN" reconcile
assert_contains "$(status_field conflicts; grep -o 'sleep_setting_drift' "$STATUS" | head -1)" "sleep_setting_drift" "status conflicts"
assert_eq "$(sleep_disabled)" "$PRE_SLEEP" "drift converged back to policy"

log "emergency-off"
"$BIN" config apply "$WORK/inside.toml" ${REPLACE[@]+"${REPLACE[@]}"} >/dev/null
wait_status reason scheduled_window || true
assert_exit 0 "emergency-off" "$BIN" emergency-off
assert_eq "$(loaded)" no "launchd unloaded"
assert_eq "$(sleep_disabled)" 0 "disablesleep forced to 0"
assert_eq "$(repeating)" "$PRE_REPEAT" "owned wake schedule released"
assert_eq "$(status_field mode)" emergency_off "status mode"
if [[ -e "/Library/Application Support/schlaflos/config.toml" ]]; then ok "configuration kept"; else fail "configuration removed by emergency-off"; fi
assert_exit 0 "doctor reports without failing" "$BIN" doctor

log "upgrade (install over existing)"
assert_exit 0 "reinstall" "$BIN" install --config "$WORK/inside.toml" ${REPLACE[@]+"${REPLACE[@]}"}
if wait_status reason scheduled_window; then ok "reconciliation after upgrade"; else fail "no reconciliation after upgrade"; fi
assert_eq "$(sleep_disabled)" 1 "disablesleep after upgrade"

log "uninstall"
assert_exit 0 "uninstall" "$BIN" uninstall
assert_eq "$(loaded)" no "launchd unloaded"
assert_eq "$(sleep_disabled)" "$PRE_SLEEP" "disablesleep restored"
assert_eq "$(repeating)" "$PRE_REPEAT" "repeating schedule restored"
for p in "/Library/LaunchDaemons/$LABEL.plist" "/Library/Application Support/schlaflos" /private/var/db/schlaflos; do
  if [[ -e "$p" ]]; then fail "$p still exists"; else ok "$p removed"; fi
done
if [[ -L /usr/local/bin/schlaflos ]]; then fail "convenience link left behind"; else ok "convenience link removed or not ours"; fi
assert_exit 0 "doctor after uninstall" "$BIN" doctor

log "result: $PASS passed, $FAIL failed"
[[ "$FAIL" == 0 ]]
