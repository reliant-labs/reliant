# Security Policy

Thanks for taking the time to help keep forge secure. This document
describes how to report vulnerabilities and what you can expect in
return.

## Supported versions

We patch security issues in the following releases:

| Version       | Supported          |
| ------------- | ------------------ |
| `main`        | :white_check_mark: |
| latest tagged | :white_check_mark: |
| older         | :x:                |

If you need a fix backported to an older release, include that in the
report and we'll discuss feasibility.

## Reporting a vulnerability

**Do not open a public GitHub issue for suspected vulnerabilities.**
Instead, email us privately:

- **Contact:** security@reliant-labs.example
- **PGP key:** _(optional — add a public-key fingerprint here if your
  project publishes one)_

Please include:

1. A description of the issue and the affected component.
2. A minimal reproduction (command, proto, or request payload).
3. Your assessment of impact (confidentiality / integrity / availability).
4. Any mitigations or workarounds you've identified.

## What to expect

- **Acknowledgement** within 3 business days of the initial report.
- **Triage** (severity + affected versions) within 10 business days.
- **Fix or mitigation** within 90 days of acknowledgement, per the
  industry-standard coordinated-disclosure window. We may request a
  brief extension for complex issues; if so we'll communicate a new
  target date.
- **Public disclosure** after a fix ships, with credit to the reporter
  unless you ask to remain anonymous.

## Scope

This policy applies to the code in this repository. Issues in
third-party dependencies should be reported upstream; we're happy to
coordinate if the patched version needs to propagate here.

## Non-security issues

For bug reports, feature requests, or general contribution questions,
see [CONTRIBUTING.md](CONTRIBUTING.md).
