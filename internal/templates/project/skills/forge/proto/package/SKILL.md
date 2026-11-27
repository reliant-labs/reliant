---
name: proto-package
description: Choose a service's proto (wire) package — the services.<svc>.v1 default, a product-scoped acme.<svc>.v1 for an API non-Go SDKs consume, or one package shared by every service — via forge.yaml api.proto_package, and the buf lint exceptions each needs.
---

# The proto package of a service

**The proto package `forge scaffold service` writes follows the project.**
In order: forge.yaml `api.proto_package` (a fixed package like
`controlplane.v1`, or a pattern like `acme.{service}.v1`); else the
convention a strict majority of the existing service protos share (an
outlier is named in the scaffold output, never silently overruled); else
`services.<service>.v1`, which mirrors the path and satisfies buf
`STANDARD` as-is. `go_package` stays `gen/services/<service>/v1` either way.

## `services.` is only the default

The wire package is the name every non-Go client sees: procedure paths
(`/<package>.<Service>/<Rpc>`) and generated SDK namespaces. A product whose
API others consume can scope it to the product:

```yaml
# forge.yaml — set before the first `forge scaffold service`
api:
  proto_package: "acme.{service}.v1"   # orders → acme.orders.v1
```

Keep the file at `proto/services/<service>/v1/<service>.proto` and keep its
`go_package` unchanged. Forge uses the service directory to name the handler
package and Buf's source-relative Go output; the wire name comes from the
compiled descriptor. Forge conventions check the service name against the
service directory, not the proto package declaration.

## Keep Buf lint on

Buf `STANDARD` expects the wire package to match the directory relative to
`proto/`. Add a scoped exception to the existing `lint:` block in `buf.yaml`:

```yaml
lint:
  use: [STANDARD]
  ignore_only:
    PACKAGE_DIRECTORY_MATCH:
      - proto/services/orders/v1/orders.proto
```

List each nonmatching directory or file, or use
`lint.except: [PACKAGE_DIRECTORY_MATCH]` if the whole project uses a different
namespace. A wire package shared across `services/*/v1/` directories also
needs `PACKAGE_SAME_DIRECTORY` and `PACKAGE_SAME_GO_PACKAGE` exceptions.
Scaffolding the second service into a shared package adds those two rules
unless `buf.yaml` is disowned. Message names must be unique in a shared package.

`api.proto_package` only applies when scaffolding. To experiment before
publishing, change the `package` declaration in your proto, configure Buf
lint as above, then run `forge generate`, `forge lint` and `go build ./...`.

## Choose it when the service is born

Changing an existing service's package changes every procedure path, which
breaks every deployed client (`buf breaking` reports it). Treat a rename like
any wire break (`proto-breaking`): serve the new package beside the old one
until clients move.
