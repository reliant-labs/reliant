---
name: go-sdk
description: Publish a Go SDK in a forge project: release the nested gen module first, pin its published version in the SDK's go.mod, and verify from a clean consumer module with GOWORK=off.
---

# Publishing a Go SDK

Put a Go SDK in a public package such as `pkg/client`. Callers can import
that package without importing your handler or database packages. Usually
it belongs in the root module, so consumers get one SDK version. Give it a
separate module only if it needs an independent release or dependency graph.
The package's public types can hide the generated protobuf types, but if
its implementation imports them, the SDK still depends on their module.

## Why it builds locally but not for a consumer

A scaffolded service project typically has two modules:

| Directory | Module path in this example | Tag for v0.3.0 |
|---|---|---|
| repo root, including `pkg/client` | `github.com/acme/dispatch` | `v0.3.0` |
| `gen/`, containing the wire types and Connect clients | `github.com/acme/dispatch/gen` | `gen/v0.3.0` |

The root requirement starts at `github.com/acme/dispatch/gen v0.0.0`. Local
builds resolve the checkout through `go.work` or `replace … => ./gen`.
Neither helps an external consumer: dependencies' `replace` directives are
ignored, and your workspace isn't theirs. A consumer downloads `gen/` as a
separate module. Tagging only the root does not publish it, even though the
Go import paths start with the same prefix.

Check `gen/go.mod` first. If it doesn't exist and `gen/` is part of the root
module (for example, in a project adopted in place that already owned its
Go stubs), there is no nested module to tag. Don't change a published
module layout just to avoid a second tag: that moves packages out of the
module consumers already require.

## Release the generated module before the SDK

1. Run `forge generate`, commit the generated files with their proto inputs,
   and make sure the commit is available in the published repo. Check
   `gen/go.mod` and its dependency versions: no local paths for modules a
   consumer needs, and each required version must be available to them.
   Verify `cd gen && GOWORK=off go test ./...`.
2. From the repository root, tag the commit containing the generated code
   and push the tag:

   ```bash
   git tag -a gen/v0.3.0 -m "Generated Go API v0.3.0" <commit>
   git push origin refs/tags/gen/v0.3.0
   ```

   The `gen/` prefix is part of the **Git tag**, not the version in `go.mod`.
3. Replace the placeholder in the SDK's module (the root in this example)
   with the resolved version, then tidy and commit `go.mod` and `go.sum`:

   ```bash
   go mod edit -require=github.com/acme/dispatch/gen@v0.3.0
   GOWORK=off go mod tidy
   ```

   A local `replace … => ./gen` can stay for in-repo development; the pin
   is what consumers see. If the generated API didn't change since the
   last release, keep its existing pin. The two module versions need not
   match. Use the `gen/v…` tag prefix for v0/v1; v2 and later need a major
   version suffix in the generated module path and imports as well (Go's
   semantic import versioning).
4. Tag and push the SDK's commit as `v0.3.0`. If you give the SDK its own
   nested `go.mod` instead, use the corresponding directory prefix on
   that tag too (e.g. `pkg/client/v0.3.0`).

## Check what a consumer actually downloads

Testing from the repo can still mask a broken release via its main-module
`replace`, even with `GOWORK=off`. Use a fresh module in a scratch directory
**outside** the repo, with no local `replace` and `GOWORK=off`:

```bash
cd "$(mktemp -d)"
export GOWORK=off
go mod init sdk-smoke
go get github.com/acme/dispatch/pkg/client@v0.3.0
go build github.com/acme/dispatch/pkg/client
go list -m github.com/acme/dispatch/gen  # must resolve v0.3.0, not a local path
```

If `go get` fails on `gen@v0.0.0`, the SDK's published `go.mod` still has
its placeholder. If it fails on `gen@v0.3.0`, check that the `gen/v0.3.0`
tag was pushed, contains `gen/go.mod` with the expected module path, and
is visible to the consumer. To fix a published release, cut a new version;
don't move tags consumers may already have downloaded.
