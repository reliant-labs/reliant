---
name: v1.0.7
description: Upgrade a project to forge v1.0.7. (1) pkg/dbquery.Options and pkg/dbquery.Event are no longer comparable — both gained an Args slice for bound query parameters. Use when Go code compares or map-keys a dbquery.Options or dbquery.Event value. (2) release.BundleFeaturePlacementPrerequisites is now "placement-prerequisites-v2" (graph version 2): no cluster kind is refused for being unknown. Use when code spells placement-prerequisites-v1 or a graph version 1.
version: v1.0.7
detection: grep -rqsE --include='*.go' --exclude-dir=gen --exclude-dir=vendor --exclude-dir=node_modules 'dbquery\.(Options|Event)\b|placement-prerequisites-v1' . && exit 0; exit 1
---

# Upgrading to v1.0.7

Each section below is independent: apply the ones whose "Use when" matches.

## 1. `dbquery.Options` and `dbquery.Event` are no longer comparable

**Use when** Go code compares a `dbquery.Options` or `dbquery.Event` with `==`/`!=`, or uses one as a map key.

**What changed.** `Options.Args` carries the values bound to `$1..$n` of the statement, and `Event.Args` records them in the audit event. A slice field makes the struct non-comparable, so the compiler now rejects those comparisons. Building and passing an `Options{}` or reading an `Event` is unaffected.

**What to do.** Compare the fields you mean (`a.MaxRows == b.MaxRows`), or use `reflect.DeepEqual` / `cmp.Equal` in tests. To key a map by options, key by the fields that matter. A comparison of the whole value is rare enough that no tool can pick the intent, so there is no codemod.

## 2. The mixed placement graph is `placement-prerequisites-v2`

**Use when** code spells the string `placement-prerequisites-v1`, or builds a
`release.PlacementPrerequisites` with `Version: 1`.

**What changed.** `release.BundleFeaturePlacementPrerequisites` is now
`"placement-prerequisites-v2"` and the graph's version is
`release.PlacementPrerequisitesVersion` (2). No cluster kind is refused for
being unknown any more: every kind but the runtime kinds
(`release.PlacementRuntimeKind`), ConfigMap and Secret is applied inventory
(`release.PlacementPassiveKind`), and a component that renders no controller
is inventory too. A v1 reader refused unknown kinds, so forge refuses a reader
that names only v1 before composition. No control plane ever advertised v1 and
no v1 graph was ever recorded, so nothing needs migrating.

**What to do.** Use the constants instead of literals:
`release.BundleFeaturePlacementPrerequisites` for the feature and
`release.PlacementPrerequisitesVersion` for the graph version. A control plane
that keeps its own passive-kind list should read `release.PlacementPassiveKind`
and `release.PlacementRuntimeKind`, so its reader admits exactly what the
forge it pins emits. Spelled literals are a choice of which contract to speak,
so there is no codemod.
