---
name: migration-v1.0.0-shape-bound
description: release.MaxShapeBytes, the bound Shape.Encode enforces on a bundle shape's canonical JSON, is 512 KiB (was 256 KiB). Use when Go code compares against release.MaxShapeBytes, or a database column that stores a bundle shape carries a size CHECK.
relevance: migration
---

# The bundle shape bound is 512 KiB

Use when Go code compares against `release.MaxShapeBytes`, or a database column
that stores a bundle shape (`Shape.Encode` output) carries a size CHECK.

### What changed

`release.MaxShapeBytes`, the bound `Shape.Encode` enforces on a shape's
canonical JSON, is 512 KiB (it was 256 KiB). A large env's shape grows with its
object count and was reaching the old bound.

### What to do

Nothing, unless you store shapes. A storing column's size CHECK must admit the
new bound plus jsonb's overhead (about 7% for a shape): `pg_column_size(shape) <
1048576` does; a CHECK sized at 256 KiB refuses shapes `Encode` now accepts.
Widen it with a forward migration before you take this release, and size any
test fixture that proves the column refuses an oversized shape from the column,
not from `MaxShapeBytes`.
