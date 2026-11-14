---
name: migration-v0.1.46-operatorkit-bridge
description: Upgrade an owned internal/app/lifecycle.go that names lifecyclekit.Run, lifecyclekit.Controller or lifecyclekit.Options to operatorkit, so importing lifecyclekit never links controller-runtime.
relevance: migration
---

# v0.1.46 — the operator bridge moved out of lifecyclekit

Use when `internal/app/lifecycle.go` calls `lifecyclekit.Run` or names
`lifecyclekit.Controller` / `lifecyclekit.Options`.

## What changed

`forge/pkg/lifecyclekit` re-exported `operatorkit.Run`, `Controller` and
`Options`, so every binary that imported lifecyclekit — for `Supervise`,
`WrapWorker` or `Singleton` — linked `sigs.k8s.io/controller-runtime` and its
Kubernetes tree, operators or not. The three names are gone from lifecyclekit;
they live only in `forge/pkg/appkit/operatorkit`, which a binary imports only
when it runs operators. Nothing else in lifecyclekit changed.

## Steps

The rename is mechanical, so forge makes it: `forge project upgrade` runs it
on its own when the project's baseline predates v0.1.46, and
`forge fix operatorkit` runs it on request (`--dry-run` lists the files it
would rewrite). Either way:

1. Every reference to `lifecyclekit.Controller`, `lifecyclekit.Options` and
   `lifecyclekit.Run` in your own Go files — `internal/app/lifecycle.go` is
   the usual one — becomes `operatorkit.Controller`, `operatorkit.Options` and
   `operatorkit.Run`. The signatures are identical.
2. `github.com/reliant-labs/forge/pkg/appkit/operatorkit` is imported where it
   is now used, and the `lifecyclekit` import is dropped from a file that no
   longer names anything else from it (`WrapWorker`, `Singleton` and
   `Supervise` stay in lifecyclekit).

By hand, the same two edits.

## Verify

`go build ./...` passes, and `go list -deps ./<a package that imports only
lifecyclekit>` no longer lists `sigs.k8s.io/controller-runtime`.
