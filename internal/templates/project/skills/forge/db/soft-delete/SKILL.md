---
name: soft-delete
description: Soft delete by convention — a time-typed deleted_at makes Delete a stamp and hides stamped rows — and the forge:no-soft-delete column declaration for a deleted_at that is an archive stamp, where archived rows stay readable and writable and Delete deletes.
---

# Soft delete, and when deleted_at is not one

## The convention

A table with a time-typed `deleted_at` column (`TIMESTAMPTZ` et al) is
soft-deleting. Nothing declares it — the column is the declaration:

- the generated struct tags it Bun's `,soft_delete`;
- `Get<E>ByID`, `List<E>`, `Count<E>` and `Update<E>` skip rows where it is set;
- `Delete<E>` stamps it instead of deleting;
- `ListAll<E>` is the one read that returns the tombstones too;
- `deleted_at` is never caller-writable: not by Update, not by an `update_mask`;
- the generated list/detail pages leave the column off (it is always empty
  on rows they show).

Birth it with `// forge:soft-delete` on the entity message, or
`forge scaffold entity --soft-delete`.

## An archive stamp: `forge:no-soft-delete`

The same column name is also how a schema spells an ARCHIVE stamp: archived
rows must stay readable and writable, and a delete must delete. Declare that
on the column, in a migration:

```sql
COMMENT ON COLUMN worktrees.deleted_at IS
    'forge:no-soft-delete — the archive stamp; archived worktrees stay readable';
```

Then `deleted_at` is an ordinary nullable timestamp everywhere forge
projects it:

| | soft delete (default) | `forge:no-soft-delete` |
|---|---|---|
| struct tag | `bun:"deleted_at,soft_delete,nullzero"` | `bun:"deleted_at"` |
| Get / List / Count | live rows only | every row |
| Update / UpdateMasked | live rows; `deleted_at` not writable | every row; `deleted_at` writable |
| Delete | stamps `deleted_at` | `DELETE` |
| `ListAll<E>` | generated | not generated |
| generated pages | column hidden | column shown |

Archive and unarchive are then ordinary writes —
`db.UpdateWorktreeMasked(ctx, tx, w, []string{"deleted_at"})` — and "live
only" is the filter your query says it is
(`orm.QueryOption(func(q *bun.SelectQuery) { q.Where("deleted_at IS NULL") })`).

Do not get here by routing queries through `WhereAllWithDeleted()` /
`ForceDelete()` on a soft-delete store: that re-derives, call by call, what the
declaration says once, and every generated path you did not reroute still hides
archived rows.

## Markers are checked

The declaration is only read on `deleted_at`. On any other column it would do
nothing, so it is rejected. That holds for every `forge:` token in a catalog
comment: an unknown marker (`forge:no-softdelete`), or a known one on an object
that does not read it (a column marker in a `COMMENT ON TABLE`), fails
`forge generate` — which reads the applied catalog — and the
`forge lint --column-markers` lane, which names the migration file and line.
`forge project annotations --kind column` and `--kind table` list what each
object reads.
