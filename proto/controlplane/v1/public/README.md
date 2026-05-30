# controlplane.v1 (public)

Public protobuf surface for the Reliant control plane. These `.proto`
files define the services the open-source Reliant client speaks to.

This directory is the **canonical home** of the public controlplane.v1
API. The (closed-source) control-plane repo consumes these files from
here via a sibling-checkout symlink — there is no separate copy in
control-plane. The TypeScript clients under
`web/src/gen/controlplane/v1/public/` are generated from this directory
by `buf.gen.controlplane.yaml`.

## Status: EXPERIMENTAL

This package is **unstable**. Field numbers, RPC names, message shapes,
and service boundaries may change without notice. There is no
stability/versioning policy yet — consumers MUST pin to a specific
reliant commit. A stable release will be announced when the contract is
frozen.

Anything under control-plane's own `controlplane/v1/internal/` is
closed-source and not part of this surface; it can change at any time.

## License

[BSL-1.1](./LICENSE) — see the parent reliant repo for the canonical
license text and FAQ.

## Consuming with buf

This module is not yet published to the BSR. Today, the control-plane
repo consumes it via a sibling checkout (the repos sit next to each
other on disk: `reliant-labs/reliant` and `reliant-labs/control-plane`).
BSR publication for downstream consumers is planned.

When that lands, downstream consumers will be able to add this module
to their `buf.yaml` deps (replace the commit pin):

```yaml
deps:
  - buf.build/reliant/reliant:<commit-sha>
```

Then import in your protos:

```proto
import "controlplane/v1/public/daemon_service.proto";
import "controlplane/v1/public/billing_service.proto";
```
