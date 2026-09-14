# Security model

Status: implemented for v1

`schlaflos` changes global macOS power settings from a root LaunchDaemon. Its
security model assumes that unprivileged users and self-hosted CI workloads may be
untrusted, even when the machine is normally used by one person.

## Trust boundaries

The operator explicitly trusts three inputs:

1. the `schlaflos` release selected for installation;
2. the administrator authorizing installation with `sudo`;
3. the root-owned installed configuration.

Repository contents, CI workspaces, process arguments, environment variables, and
files owned by an unprivileged user are not trusted by the LaunchDaemon.

The root process has no network listener, remote-control API, plugin loader, shell
integration, or arbitrary command hook. It executes only fixed macOS tools through
absolute paths with explicit argument arrays.

## Privileged installation invariants

The LaunchDaemon executes only:

```text
/Library/Application Support/schlaflos/bin/schlaflos reconcile
```

Before loading the service, the installer validates the executable,
configuration, plist, state directory, and every ancestor in their privileged
paths:

- the object is of the expected file type;
- no component is a symbolic link;
- the owner is root;
- no directory or file is group-writable or world-writable;
- the final mode matches the architecture's permission table.

An unsafe existing path causes installation to fail. The installer does not
silently take ownership of unrelated files or recursively change their modes.

The convenience command under `/usr/local/bin` is not referenced by the
LaunchDaemon and is therefore outside its executable trust chain.

## Installation trust

Elevating a downloaded program authorizes it to act as root. Release documentation
must instruct operators to verify a signed release before invoking `sudo`.
Checksums provide corruption detection but do not provide authenticity when the
checksum and binary arrive through the same compromised channel.

The project must not recommend piping network output directly into a privileged
shell, such as `curl ... | sudo sh`. v1 has no automatic self-update mechanism.

## Configuration boundary

The installed configuration is `root:wheel 0600`, decoded strictly, and validated
again by the privileged reconciler. Unknown keys are errors.

Configuration can describe schedules, power policy, and process executable paths.
It cannot describe commands, scripts, dynamic libraries, network endpoints, shell
fragments, environment expansion, or lifecycle hooks. A process executable path is
used only for comparison and is never executed.

Secrets are prohibited. In particular, GitHub runner registration tokens, API
tokens, private keys, Keychain exports, and arbitrary environment values must not
appear in configuration, state, status, or logs.

## State ownership and recovery

`state.json` is a root-only ownership ledger. It records:

- its schema version and installation identity;
- the pre-install `disablesleep` baseline;
- the pre-install recurring wake schedule;
- the last values successfully written by `schlaflos`.

The baseline is written atomically before the first system mutation. Mutations and
state updates are ordered so that an interrupted process can distinguish an owned
value from an external value on the next run.

While installed, `schlaflos` treats the settings it manages as exclusive resources
and reconciles drift to policy. On uninstall, it restores a baseline only if the
current value still matches the last value written by `schlaflos`. A mismatch is
preserved and reported rather than overwritten.

If private state is absent, corrupt, has an unsupported schema version, or has
unsafe ownership or modes, normal reconciliation performs no mutation. The
operator may inspect the system and use the explicit `emergency-off` recovery
command, which forces sleep inhibition off without pretending that the original
baseline is known.

## Status and logging

`status.json` is a separate, root-written, world-readable projection. It contains
only the information needed by `schlaflos status`:

- timestamps and reason codes;
- AC or battery state;
- requested and observed inhibition state;
- configured guard names, never full process arguments;
- wake-schedule state;
- sanitized error categories.

It contains no private ownership baselines, usernames derived from process
arguments, environment variables, credentials, or raw command output. Readers
must validate that it is a bounded-size regular file with the expected root
ownership and must not follow symlinks.

Logs follow the same redaction rules. Log destinations must be system logging or a
root-owned path; they must never be placed in the invoking user's working
directory.

## Concurrency

Manual and scheduled reconciliation can overlap. A root-owned lock under
`/var/db/schlaflos` permits only one mutation sequence at a time. Failure to
acquire the lock never falls back to an unlocked mutation.

Configuration and binary upgrades use same-directory temporary files, explicit
ownership and mode changes, validation, and atomic rename. The existing executable
remains usable if an upgrade is interrupted.

## Self-hosted runner considerations

A self-hosted CI job can execute arbitrary code as the runner account. It may be
able to consume CPU, alter user-owned files, or start another process with the same
executable path as a configured guard. A guard match therefore affects availability
only; it grants no additional privileges and causes no configured executable to be
run as root.

The runner account must not be able to modify the installed binary, configuration,
LaunchDaemon plist, private state, lock directory, or any ancestor of those paths.
Repository-level controls remain responsible for deciding which workflows are
allowed to reach the runner.

## Deliberate exclusions

v1 does not add a non-root daemon plus privileged IPC helper. For a short-lived,
non-networked reconciler, that split would introduce a new authorization protocol
and attack surface without reducing the privileges required for the actual
`pmset` mutations.

If a future GUI is introduced, macOS Service Management APIs and an authenticated
privileged helper should be evaluated as a new architecture decision rather than
retrofitted implicitly.

## References

- [Creating Launch Daemons and Agents](https://developer.apple.com/library/archive/documentation/MacOSX/Conceptual/BPSystemStartup/Chapters/CreatingLaunchdJobs.html)
- [Updating helper executables from earlier versions of macOS](https://developer.apple.com/documentation/servicemanagement/updating-helper-executables-from-earlier-versions-of-macos)
- [Schedule your Mac to turn on or off in Terminal](https://support.apple.com/guide/mac-help/mchl40376151/mac)
