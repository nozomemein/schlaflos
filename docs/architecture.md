# schlaflos architecture

Status: implemented for v1 (delivery plan steps 1-6); signed releases are still pending

## 1. Purpose

`schlaflos` keeps a macOS machine available during configured time windows and
while selected local workloads are running. It can also configure a recurring
macOS wake event. A typical use case is a Mac that serves as a self-hosted CI
runner during the day but should otherwise retain normal sleep behavior.

The first release is intentionally macOS-only. It favors a small, auditable
privileged surface over cross-platform abstractions or a plugin system.

## 2. Technology choice

v1 will be written in Go.

The program is primarily configuration parsing, deterministic policy evaluation,
process inspection, and invocation of a small set of macOS commands. Go provides
a straightforward single-binary distribution model and can execute fixed argument
vectors without invoking a shell. Rust would also be viable, but it adds complexity without materially
improving the first version's safety or behavior. The one direct IOKit call
(one-off wake events) is a small cgo binding around three functions.

This decision can be revisited if the roadmap gains either of these requirements:

- direct and extensive macOS framework integration;
- a committed Linux or Windows implementation.

## 3. System model

The installed system has two execution paths:

1. A user invokes `schlaflos` to validate configuration, inspect status, install,
   update, or remove the service.
2. `launchd` invokes `schlaflos reconcile` at a fixed interval. Each invocation is
   short-lived: it reads current inputs, calculates the desired state, applies the
   minimum required change, writes a status snapshot, and exits.

There is no permanently running custom daemon in v1. `launchd` already provides
scheduling, crash handling, and ownership of the privileged process lifecycle.

```text
config.toml ───────────────┐
clock ─────────────────────┤
AC/battery state ──────────┼─> policy ─> desired state ─> macOS power adapter
matching processes ────────┘                    │
                                                ├─> private state.json
                                                └─> redacted status.json

launchd ── every N seconds ──> schlaflos reconcile
pmset   ── recurring event ──> wake or power on
```

`launchd` intervals missed while the machine is asleep are not timers that wake
the machine. Scheduled wake is therefore configured separately through `pmset`.

### Sleep-inhibition mechanism

v1 targets closed-lid Mac operation and therefore manages the system-wide
`disablesleep` power setting rather than relying on a short-lived idle-sleep
assertion. The macOS adapter invokes `/usr/bin/pmset` with fixed arguments and
verifies the observed setting after every change.

Because this is a global setting, installation records its pre-install value as
the baseline. Releasing a `schlaflos` inhibition restores that baseline instead of
blindly writing `0`. Uninstall follows the same rule. While installed,
`schlaflos` is expected to be the only manager of `disablesleep`; `doctor` reports
an external change as a conflict.

The baseline must be persisted atomically before the first power-setting mutation.
The private state also records the last value written by `schlaflos`. If the
observed value later differs, reconciliation records the conflict and converges it
to the current effective policy while the service remains installed. Uninstall
restores the baseline only when the observed value still matches the last value
written by `schlaflos`; otherwise it leaves the external value unchanged and
reports the conflict.

## 4. Policy

The central decision is a pure function that calculates whether `schlaflos`
itself requests inhibition:

```text
prevent_sleep = power_allowed
                AND (inside_any_window OR any_process_guard_active)
```

Where:

- `power_allowed` is true when AC power is connected, or when `require_ac` is
  disabled;
- `inside_any_window` is true when local wall-clock time is inside at least one
  configured window;
- `any_process_guard_active` is true when at least one configured process guard
  matches a running process.

`require_ac` is an absolute safety condition. An active process guard does not keep
the machine awake on battery when `require_ac = true`.

When the request is false, the reconciler restores the recorded baseline. A
pre-existing baseline of `disablesleep = 1` therefore remains enabled; the status
output distinguishes the policy request from the effective system setting.

For the common self-hosted runner setup, a window can keep the Mac available from
08:00 to 20:00 every day, while a guard matching `Runner.Worker` lets a job that
started before 20:00 finish. Removing AC power releases the `schlaflos` inhibition
request immediately; the effective setting then returns to the recorded baseline.

### Window semantics

- Times use the Mac's current local timezone.
- Start is inclusive and end is exclusive.
- Windows may cross midnight.
- Day names refer to the day on which the window starts.
- Daylight-saving transitions follow the operating system's local-time rules.
- Unknown fields, malformed days, zero-length windows, and invalid durations are
  configuration errors.

Policy evaluation returns a stable reason code in addition to the desired state:

| Reason | Meaning |
| --- | --- |
| `scheduled_window` | inside a configured window; inhibition requested |
| `active_process_guard` | outside every window, but a guard matches; inhibition requested |
| `battery_power` | a window or guard applies, but the Mac is on battery and `require_ac` is set |
| `outside_window` | no window and no guard applies |

The reconciler adds two codes of its own outside the policy function:
`invalid_configuration` when the installed configuration fails validation and
`power_source_unknown` when the power source cannot be read.

## 5. Wake scheduling

Wake scheduling is independent of sleep inhibition. Two mechanisms exist:

1. One recurring `wakeorpoweron` schedule through `pmset repeat`, which macOS
   limits to a single repeating on/off pair. It is the durable daily anchor: it
   survives however long the Mac stays off.
2. Optional one-off wake events reserved through the IOKit
   `IOPMSchedulePowerEvent` API at every `wake.interval` inside each window,
   for the next 24 hours. They bring a Mac that fell asleep during a window
   back within one interval, after a missed anchor wake, a manual sleep, or AC
   power that was removed and later restored. The reconciliation that runs on
   wake then decides whether the Mac stays awake; on battery with `require_ac`
   it returns to sleep after the idle timer.

The one-off events carry `io.github.nozomemein.schlaflos` as their owner
identifier. Ownership is therefore established by the event itself, not by the
ledger: reconciliation lists the owned events, cancels those outside the plan,
and reserves the missing ones; `emergency-off` and `uninstall` cancel every
owned event and never touch events of other owners. The API is a user-space
IOKit call made through cgo; no system extension is involved.

The wake schedule is reconciled idempotently whenever `schlaflos` is already
running: the current `pmset` schedule is read, compared with the desired schedule,
and changed only when necessary. The private state records both the pre-install
baseline and the last schedule written by `schlaflos`. This guarantees the wake
reservation, not the outcome of a past wake attempt.

A sleeping Mac cannot run a local periodic check. Consequently, `launchd` cannot
notice at 08:15 that an 08:00 wake was missed and wake the same machine. Supporting
that recovery case would require an external always-on coordinator, such as a
separate host capable of sending an appropriate network wake request. That is
outside the v1 scope.

After any boot or wake, including a late manual wake, reconciliation runs
immediately. If the current time is inside an active window and the power policy
allows it, sleep inhibition is enabled without waiting for the next interval.

Installation must read the current schedule before changing it. If an unrelated
schedule exists, `schlaflos` refuses to replace it unless the operator supplies
`--replace-wake-schedule`. While installed, `schlaflos` treats the recurring wake
schedule as an exclusively managed global resource and repairs drift. Uninstall
restores the pre-install schedule only when the current schedule still matches the
last value written by `schlaflos`; otherwise it preserves the external value and
reports a conflict.

A wake event can wake a sleeping Mac with power available. It cannot compensate
for an unplugged and depleted machine, and a FileVault-protected Mac that fully
powers off may still require interactive login.

## 6. Privilege and security boundary

The reconciler runs as root because changing system power behavior and installing
a LaunchDaemon require elevated privileges. The privileged surface is deliberately
narrow:

- no shell is invoked;
- subprocesses use fixed executable paths and explicit argument arrays;
- configuration cannot define arbitrary commands or hooks;
- privileged paths do not expand `~` or environment variables;
- installed binaries and configuration are not user-writable;
- every component of a privileged path is verified as root-owned and not
  group-writable or world-writable before use;
- writes use a temporary file, ownership and mode checks, then atomic rename;
- symlinks are rejected for privileged configuration and state paths;
- status redacts process arguments and reports only configured guard names;
- configuration, state, status, and logs never contain credentials or other
  secrets;
- the LaunchDaemon receives a minimal environment and invokes all subprocesses by
  absolute path.

Configuration is validated before elevation for fast feedback and again by the
privileged process before installation or application. An invalid installed
configuration fails safe by restoring the recorded baseline and recording an error
rather than continuing a stale `schlaflos` request indefinitely.

Public repositories make this boundary especially important: configuration must
never turn the root service into a generic command runner.

The complete trust boundary, installation invariants, and recovery rules are
documented in [`security.md`](security.md).

## 7. Filesystem layout

```text
/usr/local/bin/schlaflos
    User-facing convenience executable. launchd never targets this path.

/Library/Application Support/schlaflos/bin/schlaflos
    Root-owned executable invoked by launchd using this fixed absolute path.

/Library/Application Support/schlaflos/config.toml
    Root-owned installed configuration.

/Library/LaunchDaemons/io.github.nozomemein.schlaflos.plist
    Root-owned launchd job.

/var/db/schlaflos/state.json
    Private ownership ledger: baselines, last applied values, and schema version.

/var/db/schlaflos/status.json
    Redacted status projection readable by unprivileged users.

/var/db/schlaflos/reconcile.lock
    Root-owned lock preventing concurrent manual and launchd reconciliation.

/var/log/schlaflos.log
    launchd standard output and error of the reconciler.
```

On macOS `/var` is a symbolic link to `/private/var`. Because symbolic links are
rejected anywhere in a privileged path chain, the implementation addresses every
path under `/var` through its canonical `/private/var` form. The lock lives next
to the ledger rather than under `/var/run` because `/private/var/run` is
`root:daemon 0775`, which violates the group-writable rule, and is emptied at boot.

The installation directory and every privileged ancestor must be root-owned and
must not be group-writable or world-writable. The installer fails rather than
repairing or trusting an unsafe ancestor. The installed modes are:

| Artifact | Owner | Mode |
| --- | --- | --- |
| Privileged directories | `root:wheel` | `0755` |
| Root-executed binary | `root:wheel` | `0755` |
| Installed configuration | `root:wheel` | `0600` |
| LaunchDaemon plist | `root:wheel` | `0600` |
| Private state | `root:wheel` | `0600` |
| Redacted status | `root:wheel` | `0644` |
| Reconciliation lock | `root:wheel` | `0600` |

`/usr/local/bin/schlaflos` is not part of the LaunchDaemon trust chain. It may be a
copy or symlink installed for interactive use, but the plist always names the
root-owned binary under `/Library/Application Support`.

## 8. Command-line interface

Proposed public commands:

```text
schlaflos init [PATH]
schlaflos config check PATH
schlaflos status [--json]
schlaflos doctor
sudo schlaflos install --config PATH [--replace-wake-schedule]
sudo schlaflos config apply PATH [--replace-wake-schedule]
sudo schlaflos emergency-off
sudo schlaflos uninstall
```

Internal/service command:

```text
schlaflos reconcile [--dry-run]
```

`schlaflos version` prints the build version. `reconcile --dry-run` evaluates
and prints the decision without acquiring ownership of anything; it still needs
root because the installed configuration and ledger are `0600`.

`emergency-off` explicitly forces `disablesleep = 0`, removes the wake schedule
only when it is still owned by `schlaflos`, and unloads the LaunchDaemon without
deleting configuration. This command intentionally overrides the recorded sleep
baseline and exists as a recovery control. `uninstall` conditionally restores the
recorded pre-install baselines, then removes only artifacts owned by `schlaflos`.

`status` reports:

- evaluation time and next expected transition;
- desired and observed inhibition state;
- policy reason code;
- AC or battery state;
- active configured guard names;
- last successful transition and last error;
- installed and observed wake schedule.

The command reads only the redacted `status.json`. It never needs permission to
read the private ownership ledger.

## 9. Configuration

The v1 configuration format is versioned TOML. See
[`examples/schlaflos.toml`](../examples/schlaflos.toml).

Strict decoding is required: unknown keys are rejected rather than ignored. This
prevents a typo in a safety-related option from silently changing behavior.

Secrets are not valid configuration values. Authentication tokens, signing
material, runner credentials, and arbitrary environment variables are outside the
schema and must never be copied into the installed configuration.

`wake.interval` enables the one-off wake events described in section 5. It
must be a whole number of minutes between 5 minutes and 12 hours and requires at
least one window, because events are reserved inside windows only.

Process guards match a canonical executable path, not a substring of a shell
command line. The snapshot comes from `/bin/ps -axo pid=,comm=`, whose `comm`
column on macOS is the executable path passed to `execve`; the configured path
must therefore be the absolute path the workload is actually launched with. This reduces false positives and avoids exposing full arguments in
status output. More guard kinds may be added later, but v1 does not expose a plugin
or arbitrary-script interface.

## 10. Internal package boundaries

```text
cmd/schlaflos/                 CLI entry point
internal/config/               TOML schema, decoding, validation
internal/policy/               pure desired-state calculation and reason codes
internal/reconcile/            orchestration and transition logic
internal/install/              install, config apply, uninstall, emergency-off
internal/doctor/               read-only installation checks
internal/processguard/         process snapshot and executable matching
internal/state/                private ledger, redacted status, atomic persistence
internal/layout/               privileged paths, label, and mode table
internal/safefs/               path-chain verification, atomic writes, lock
internal/platform/macos/cmdrun/ fixed-path subprocess execution and test fake
internal/platform/macos/power/ AC state and sleep-inhibition adapter
internal/platform/macos/wake/  pmset schedule inspection and mutation
internal/platform/macos/wakeevents/ one-off wake events through IOKit (cgo)
internal/platform/macos/launchd/ plist rendering and service management
packaging/launchd/             embedded LaunchDaemon plist template
examples/                      example configuration
docs/                          architecture and operational documentation
```

Interfaces should exist only at real operating-system boundaries: clock, power
state, process inspection, wake scheduling, and state persistence. The policy
package accepts values and returns a decision; it does not know about commands,
files, or macOS.

## 11. Reconciliation and failures

Each reconciliation follows this sequence:

1. Acquire the root-owned reconciliation lock without following symlinks.
2. Open and strictly validate the installed configuration and private state.
3. Read local time, power source, relevant processes, and observed sleep state.
4. Evaluate policy without side effects.
5. Apply a transition only if desired and observed states differ.
6. Persist private state, when changed, and a redacted status snapshot atomically.
7. Release the reconciliation lock and exit.

Failure behavior:

- Invalid configuration: restore the recorded pre-install baseline, record the
  validation error, exit non-zero.
- Missing or corrupt private state: make no power or wake-schedule mutation, write
  an error through the protected logging path, and require explicit recovery.
- Power-source read failure: make no power mutation, record the error, exit
  non-zero.
- Process inspection failure: do not treat guards as active; record a degraded
  status so the operator can see the loss of protection.
- Mutation failure: retain the observed state, record the error, exit non-zero.
- State-file failure: log to the launchd standard-error path and exit non-zero.
- Concurrent invocation: leave mutation to the lock holder and exit without
  changing state.

Before every mutation the reconciler persists the intended value as a pending
write; after the mutation is verified it becomes the last written value. On the
next run a pending write whose value is observed is adopted as owned, and one
whose value is not observed is discarded. This is how an interrupted process is
distinguished from an external change.

`launchd` retries at the next interval. Reconciliation must be idempotent so that
retries are harmless.

## 12. Testing strategy

The most important tests are policy table tests covering:

- AC versus battery power;
- before, at, and after window boundaries;
- weekdays, weekends, overnight windows, and timezone transitions;
- an active process after a window ends;
- invalid and overlapping configuration;
- stable reason codes.

Platform adapters use captured command output and fixed argument assertions.
Additional tests cover atomic state writes, symlink rejection, plist rendering,
ownership and mode validation for complete path chains, concurrent invocation,
corrupt-state recovery, drift detection, and redaction of public status.

Privileged integration tests run only on a dedicated Mac or a throwaway macOS
VM (`scripts/integration-privileged.sh`, wrapped for Tart by
`scripts/integration-vm.sh`). They verify install, repeated reconciliation,
wake-schedule handling, drift convergence, `emergency-off`, upgrade, and
uninstall. A shared CI runner must not have its global power settings modified
by the test suite.

## 13. Delivery plan

1. Define the versioned configuration model and pure policy package. (done)
2. Add read-only macOS adapters plus `config check`, `status`, and `doctor`. (done)
3. Implement `reconcile --dry-run` and durable state snapshots. (done)
4. Add sleep-state mutation and `emergency-off`. (done)
5. Add installation, LaunchDaemon management, and guarded wake scheduling. (done)
6. Exercise privileged integration tests on a dedicated Mac. (done on the maintainer's Mac, 2026-09-14, 48/48 with `scripts/integration-privileged.sh`)
7. Publish signed `darwin/arm64` and `darwin/amd64` binaries with checksums.

## 14. Deliberate non-goals for v1

- Linux or Windows support;
- remote control or a network API;
- arbitrary scripts, lifecycle hooks, or plugins;
- a graphical interface;
- more than one recurring `pmset repeat` schedule;
- automatic self-update.

These omissions keep the initial public release small enough to audit and make its
root behavior understandable from the configuration alone.
