---
name: closed-sets
description: Keep a CHECK constraint and the Go set it mirrors equal — declare `forge:closed-set <set>` on the constraint, and forge lint's closed-sets lane names missing and extra values and prints (or `forge db migration new --closed-set` writes) the forward migration.
---

# A CHECK that mirrors a Go set

When a CHECK lists a vocabulary that Go also owns, such as forge's access-token
scopes, the two must not drift. If Go gains a value the CHECK lacks, every
write of it fails the constraint. If the CHECK admits a value Go lacks, rows
holding it cannot be read. Declare which set the CHECK mirrors, on the CHECK:

```sql
COMMENT ON CONSTRAINT access_tokens_scopes ON access_tokens IS
    'forge:closed-set accesstoken.AllScopes';
```

`forge lint` checks every declaration in its closed-sets lane, which runs by
default (`--closed-sets` runs it alone). It finds the CHECK the migrations last
defined and reads its list. It reads `col <@ ARRAY[…]` for an array column,
and `col = ANY (ARRAY[…])` or `col IN (…)` for a scalar, with or without casts.
If the list differs from the set, the lane names the missing and extra values
and prints the forward migration that fixes it.
`forge db migration new --closed-set <constraint>` writes that migration for
you. The lane needs no database, so it runs on every tier.

- **Set names.** forge exports `accesstoken.AllScopes` and
  `accesstoken.AllResourceKinds`. Your own set is named by its qualified Go
  name, `<import path>.<Var>`: a package-level variable initialized by a
  literal of constants. forge type-checks the package and runs none of its
  code. An unknown name fails the lane.
- **Re-declare after replacing the CHECK.** postgres drops a constraint's
  comment along with the constraint. A migration that replaces the CHECK with
  DROP + ADD must therefore re-issue the COMMENT, and the fix migration does.
  The lane reports a declaration that was lost this way. To retire one on
  purpose, issue a COMMENT without the marker.
- **A widening never scans under its lock.** Every existing row satisfied the
  narrower CHECK, so the fix adds the widened one `NOT VALID`: the DROP + ADD
  holds ACCESS EXCLUSIVE for the catalog change alone. A second migration,
  `<name>_validate`, runs `VALIDATE CONSTRAINT` in its own transaction, under
  SHARE UPDATE EXCLUSIVE, which blocks neither reads nor writes. A narrowing
  stays one validated ALTER: the rows it refuses must be rewritten first, in
  the same migration, and the ALTER proves none was missed.

## Access-token scopes: a format check is the other option

`accesstoken.AllScopes` grows with forge releases, so a scope column that
declares it pays a widening and a validate migration for every new scope, and
cannot store a `bundle:<name>` reference at all. A store may instead CHECK the
FORMAT of each entry (`^[a-z]+(:[a-z]+)*$`, which `bundle:<name>` matches
too) and leave the vocabulary to the write path, which already refuses an
unknown scope, with a tolerant read (`accesstoken.ResolveStored`) so a rollback
binary skips what a newer one wrote. A CHECK cannot test array elements one by
one, so the SQL must also refuse an element holding a comma and a NULL one,
which a regex over `array_to_string` alone lets through. The
`forge:closed-set accesstoken.AllScopes` declaration keeps working for a store
that wants the database to enumerate the set. `forge skill load auth/scopes`
has the trade-off and the SQL.
