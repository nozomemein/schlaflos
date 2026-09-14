# schlaflos

`schlaflos` is a configuration-driven macOS CLI that keeps a machine awake during
scheduled hours or while selected workloads are running. It can also configure a
recurring wake schedule, making an otherwise idle Mac suitable for unattended CI
and other long-running jobs.

The project is currently in the design phase. The first release will target macOS
and ship as a single Go binary managed by `launchd`.

## Design goals

- Keep sleep prevention predictable and observable.
- Never inhibit sleep on battery power when `require_ac` is enabled.
- Allow an active workload to finish after a scheduled window ends.
- Keep privileged behavior narrow: no shell execution or arbitrary root hooks.
- Install and remove cleanly without overwriting unrelated `pmset` schedules.

## Proposed usage

```sh
schlaflos config check ./schlaflos.toml
sudo schlaflos install --config ./schlaflos.toml
schlaflos status
```

See the [architecture](docs/architecture.md) and
[example configuration](examples/schlaflos.toml) for the proposed v1 design.

## Status

The command-line interface and configuration format documented here are a design
contract for the initial implementation. They may change before the first tagged
release.

## License

[MIT](LICENSE)
