# schlaflos

`schlaflos` is a configuration-driven macOS CLI that keeps a machine awake during
scheduled hours or while selected workloads are running. It can also configure a
recurring wake schedule, making an otherwise idle Mac suitable for unattended CI
and other long-running jobs.

It ships as a single Go binary. A root LaunchDaemon runs `schlaflos reconcile`
at a fixed interval; each run reads the configuration, evaluates the policy, and
applies the minimum change to the system-wide `pmset disablesleep` setting.

## Design goals

- Keep sleep prevention predictable and observable.
- Never inhibit sleep on battery power when `require_ac` is enabled.
- Allow an active workload to finish after a scheduled window ends.
- Keep privileged behavior narrow: no shell execution or arbitrary root hooks.
- Install and remove cleanly without overwriting unrelated `pmset` schedules.

## Install

```sh
go install github.com/nozomemein/schlaflos/cmd/schlaflos@latest
```

This needs Go 1.26 and the Xcode Command Line Tools (the binary links IOKit
through cgo). Or build from a checkout with `just build`.

## Usage

```sh
schlaflos init                                  # writes ./schlaflos.toml
schlaflos config check ./schlaflos.toml
sudo schlaflos install --config ./schlaflos.toml   # copies the binary into place
schlaflos status
schlaflos doctor
```

The binary you run `install` from can live anywhere: it is copied to
`/Library/Application Support/schlaflos/bin/schlaflos` and linked from
`/usr/local/bin/schlaflos`.

Set `wake.interval` (for example `"1h"`) to reserve extra wake events inside
each window so a Mac that fell asleep, or lost and regained AC power, is back
within one interval.

Later changes go through `sudo schlaflos config apply PATH`. `sudo schlaflos
emergency-off` forces normal sleep and unloads the service; `sudo schlaflos
uninstall` restores the pre-install settings it still owns and removes every
artifact. See [operations](docs/operations.md) for the full lifecycle.

## Building

```sh
just build          # ./bin/schlaflos
just check          # gofmt, go vet, go test
just test-readonly  # also queries this Mac's pmset read-only
```

Go 1.26 and [`just`](https://github.com/casey/just) are required. The test suite
uses fakes for `pmset` and `launchctl` and never changes power settings.

## Documentation

- [Architecture](docs/architecture.md): system model, policy, wake scheduling,
  filesystem layout, and package boundaries.
- [Security model](docs/security.md): trust boundaries, installation
  invariants, and recovery rules.
- [Operations](docs/operations.md): install, upgrade, inspect, recover, remove.
- [Example configuration](examples/schlaflos.toml).

## Status

The v1 implementation is complete for the documented command set and has passed
the privileged integration test on a real Mac. Signed releases are still pending, so
verify the behavior on a machine you control before relying on it.

## Security

See the [security policy](SECURITY.md) to report a vulnerability.

## License

[MIT](LICENSE)
