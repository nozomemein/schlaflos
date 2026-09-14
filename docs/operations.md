# Operations

This guide covers the lifecycle of an installation. The architecture and
security documents explain why each step behaves as it does.

## Requirements

- macOS on Apple silicon or Intel.
- An administrator account for `sudo`.
- A workload whose executable path is stable, if process guards are used.

## Get the binary

Either build from a checkout or let the Go toolchain fetch and build it:

```sh
go install github.com/nozomemein/schlaflos/cmd/schlaflos@latest
sudo "$(go env GOPATH)/bin/schlaflos" install --config ./schlaflos.toml
```

The binary links IOKit through cgo, so `go install` needs the Xcode Command
Line Tools (`xcode-select --install`). `install` copies the running binary
into `/Library/Application Support/schlaflos/bin`, so the `go install` output
can live anywhere and is not referenced afterwards.

## Write and validate a configuration

```sh
schlaflos init
$EDITOR schlaflos.toml
schlaflos config check schlaflos.toml
```

`config check` rejects unknown keys, malformed days, zero-length windows, and
invalid durations. The example configuration keeps the Mac awake 08:00-20:00
every day on AC power, wakes it at 08:00, and lets a running GitHub Actions job
finish after 20:00.

Process guards compare the exact executable path of running processes. For a
GitHub Actions runner installed at `/Users/ci/actions-runner`, the job process
is `/Users/ci/actions-runner/bin/Runner.Worker`. Check the path on the machine:

```sh
ps -axo pid=,comm= | grep Runner.Worker
```

### Interval wakes

Inside a window `disablesleep = 1` prevents idle and lid-close sleep, but a
Mac can still end up asleep: the daily wake failed, someone chose Sleep from
the Apple menu, or AC power was removed and later restored (the reconciler
drops its request within one poll interval on battery when `require_ac` is
set, and this hardware offers no wake-on-power-change setting). Set
`wake.interval` to reserve additional one-off wake events inside every window:

```toml
[wake]
enabled = true
days = ["mon", "tue", "wed", "thu", "fri", "sat", "sun"]
time = "08:00"
interval = "1h"
```

The Mac is then back within one interval, and the reconciliation that runs on
wake decides whether it stays up. On battery with `require_ac = true` it
sleeps again after the idle timer, so the cost is a minute or two per
interval. With `require_ac = false` it stays awake until the window ends,
even in a bag. The events show up in `pmset -g sched` with the owner
`io.github.nozomemein.schlaflos`.

## Install

```sh
sudo schlaflos install --config ./schlaflos.toml
```

Installation:

1. validates the configuration;
2. records the current `disablesleep` value and recurring `pmset` schedule as
   baselines in `/var/db/schlaflos/state.json`;
3. copies the binary to `/Library/Application Support/schlaflos/bin/schlaflos`,
   installs the configuration and the LaunchDaemon plist, and links
   `/usr/local/bin/schlaflos` when nothing else is there;
4. installs the wake schedule when `[wake]` is enabled;
5. loads `io.github.nozomemein.schlaflos`, which reconciles immediately.

If an unrelated recurring `pmset` schedule already exists and `[wake]` is
enabled, installation stops before writing anything. Re-run with
`--replace-wake-schedule` to take it over; uninstall restores it.

The installer refuses to run when any ancestor directory of its artifacts is
not root-owned or is group- or world-writable. It never repairs such a path.

## Inspect

```sh
schlaflos status          # human-readable
schlaflos status --json   # the raw redacted projection
schlaflos doctor          # invariants, drift, freshness
sudo schlaflos doctor     # also checks the configuration, launchd, and ledger
```

`status` distinguishes the schlaflos request from the effective setting. A
pre-existing `disablesleep = 1` baseline stays enabled outside the windows, and
`status` shows `request: normal sleep` with `observed: disablesleep=1`.

Reconciler logs go to `/var/log/schlaflos.log`.

```sh
sudo schlaflos reconcile --dry-run   # evaluate now without changing anything
```

## Change the configuration

```sh
schlaflos config check ./schlaflos.toml
sudo schlaflos config apply ./schlaflos.toml
```

`config apply` replaces the installed configuration, rewrites the plist if the
poll interval changed, applies wake-schedule changes with the same conflict
guard as `install`, and triggers an immediate reconciliation. Disabling
`[wake]` hands the pre-install schedule back as long as the current schedule is
still the one schlaflos wrote.

## Upgrade

Run `sudo schlaflos install --config PATH` with the new binary. Baselines and
the install identity are kept; the binary, configuration, and plist are
replaced atomically and the service is reloaded.

## Drift and conflicts

schlaflos treats `disablesleep` and the recurring wake schedule as exclusively
managed while installed. If either is changed externally, the next
reconciliation records a conflict in `status` and converges the value back to
policy. `doctor` reports the same drift before the next run.

Uninstall and emergency-off take the opposite stance: a value that no longer
matches what schlaflos last wrote is preserved and reported, never overwritten.

## Recover

```sh
sudo schlaflos emergency-off
```

Forces `disablesleep = 0`, releases the wake schedule when schlaflos still owns
it, and unloads the service. The configuration stays installed; run
`sudo schlaflos config apply PATH` to resume.

If `/var/db/schlaflos/state.json` is missing or corrupt, reconciliation performs
no mutation and logs the reason. Recover with `emergency-off`, remove the state
file, and reinstall.

## Uninstall

```sh
sudo schlaflos uninstall
```

Unloads the service, restores the recorded `disablesleep` and wake-schedule
baselines when the current values are still the ones schlaflos wrote, and
removes only the artifacts it installed.

## Limitations

- A sleeping Mac cannot notice a missed wake. Wake events are reservations,
  not guarantees, and a depleted or powered-off machine stays off.
- A FileVault-protected Mac that fully powers off may need interactive login.
- `pmset` supports one recurring on/off pair; schlaflos manages that pair as a
  whole.
- `pmset -g sched` prints some weekday combinations as "Some days". schlaflos
  recovers the exact days from the powerd preference file when it can; if it
  cannot, uninstall cancels such a pre-install schedule instead of restoring it
  and says so during installation.

## Integration testing

The unit tests never touch the machine. The privileged lifecycle is exercised
by `scripts/integration-privileged.sh`, which installs, reconciles, applies a
new configuration, provokes drift, runs `emergency-off`, upgrades, and
uninstalls, asserting the `pmset` and `launchctl` state at each step and that
the pre-test values are restored at the end.

Run it only on a throwaway system. Two entry points exist:

```sh
just integration-vm           # inside a Tart macOS VM; the host is untouched
just integration-privileged   # on this Mac; only for a dedicated machine
```

`integration-vm` needs Apple silicon, [Tart](https://tart.run), and roughly
25 GB of free disk for the base image on the first run. The VM is deleted
afterwards unless `KEEP_VM=1` is set. Docker and Apple Container cannot host
this test: both run Linux guests, and the test needs macOS `powerd` and
`launchd`.

A VM always reports AC power, so battery behavior is covered by the policy
unit tests only.
