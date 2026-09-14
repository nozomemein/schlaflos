# Operations

This guide covers the lifecycle of an installation. The architecture and
security documents explain why each step behaves as it does.

## Requirements

- macOS on Apple silicon or Intel.
- An administrator account for `sudo`.
- A workload whose executable path is stable, if process guards are used.

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
