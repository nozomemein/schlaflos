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

## Usage

```sh
schlaflos init                                  # writes ./schlaflos.toml
schlaflos config check ./schlaflos.toml
sudo schlaflos install --config ./schlaflos.toml
schlaflos status
schlaflos doctor
```

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

The v1 implementation is complete for the documented command set. Privileged
integration testing on a dedicated Mac and signed releases are still pending, so
verify the behavior on a machine you control before relying on it.

## Security

See the [security policy](SECURITY.md) to report a vulnerability.

## License

[MIT](LICENSE)
