# Security policy

`schlaflos` manages global macOS power settings from a root LaunchDaemon. Security
reports are welcome, especially when they involve a privilege boundary, unsafe
installation behavior, or unintended system-wide changes.

For the design assumptions and trust boundaries, see the
[security model](docs/security.md).

## Supported versions

`schlaflos` is currently in the design phase and has no supported release. Reports
against the documented design or the current default branch are still useful and
will be handled on a best-effort basis.

This section will be replaced with a release support table when the first version
is published.

## Reporting a vulnerability

Do not include vulnerability details, proof-of-concept code, credentials, private
paths, or personal information in a public issue.

GitHub private vulnerability reporting is the intended reporting channel, but it
is not enabled for this repository yet. Until it is available:

1. Open a public issue titled `Security contact request`.
2. Include only a brief, non-sensitive category, such as `installer privilege
   boundary` or `status information disclosure`.
3. Wait for the maintainer to arrange a private channel before sharing technical
   details.

Please include the following information privately when a channel is established:

- the affected commit or release;
- the macOS version and hardware architecture;
- required privileges and preconditions;
- reproducible steps or a minimal proof of concept;
- the observed and expected behavior;
- the likely impact;
- any suggested mitigation;
- whether the issue has been disclosed elsewhere.

Remove tokens, runner credentials, private keys, and unrelated personal data from
logs and examples before sending them.

## Issues of particular interest

Examples include:

- executing attacker-controlled code as root;
- replacing or influencing the LaunchDaemon executable through an unsafe path;
- symlink, ownership, permission, or time-of-check/time-of-use attacks;
- modifying privileged configuration or private state without authorization;
- bypassing AC-power safety policy;
- failing to restore or preserve pre-existing `pmset` state safely;
- exposing secrets or raw process arguments through status or logs;
- unsafe installation, upgrade, or uninstall behavior;
- accepting arbitrary commands, shell fragments, or dynamic code through
  configuration.

## Generally out of scope

The following are normally not vulnerabilities by themselves:

- a self-hosted workflow performing actions already permitted to the runner
  account;
- a configured process guard keeping an AC-powered Mac awake as documented;
- wake failures caused by the Mac being unplugged, powered off, or subject to
  hardware or macOS limitations;
- reports that require an administrator to intentionally run an untrusted binary
  with `sudo`;
- missing hardening that has no demonstrated security impact.

These cases may still be reported as normal bugs or design feedback when they
produce surprising behavior.

## Response and disclosure

This is currently a personal, pre-release project and does not offer a guaranteed
response or remediation time. The maintainer will make a best-effort attempt to:

1. acknowledge a private report;
2. reproduce and assess its impact;
3. agree on a disclosure timeline with the reporter;
4. prepare a fix and regression coverage before public disclosure;
5. credit the reporter if requested.

Please do not publish details that would put users at risk before a fix or
mitigation is available. Once releases exist, qualifying fixes should be published
with a GitHub security advisory and clear upgrade guidance.
