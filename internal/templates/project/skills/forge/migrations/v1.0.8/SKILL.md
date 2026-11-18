---
name: v1.0.8
description: Upgrade a project to forge v1.0.8. pkg/migratekit.Grants is no longer comparable — it gained a SecurityDefiner slice naming the SECURITY DEFINER functions a granted role can execute. Use when Go code compares or map-keys a migratekit.Grants value.
version: v1.0.8
detection: grep -rqsE --include='*.go' --exclude-dir=gen --exclude-dir=vendor --exclude-dir=node_modules 'migratekit\.Grants\b' . && exit 0; exit 1
---

# Upgrading to v1.0.8

Each section below is independent: apply the ones whose "Use when" matches.

## 1. `migratekit.Grants` is no longer comparable

**Use when** Go code compares a `migratekit.Grants` with `==`/`!=`, or uses one as a map key.

**What changed.** `Grants.SecurityDefiner` lists the SECURITY DEFINER functions a granted role (read-only or dashboard) can execute — a way around its column grants, reported as a warning for the caller to log. A slice field makes the struct non-comparable, so the compiler now rejects those comparisons. Reading `Result.Grants` and its fields is unaffected; the same release adds `DashboardApplied`, `DashboardRoleDisabled` and `DashboardRoleMissing` for the opt-in `forge_dash` role.

**What to do.** Compare the fields you mean (`g.Applied == want.Applied`), or use `reflect.DeepEqual` / `cmp.Equal` in tests. A comparison of the whole value is rare enough that no tool can pick the intent, so there is no codemod.
