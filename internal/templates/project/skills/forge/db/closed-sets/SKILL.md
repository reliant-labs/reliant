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
  The lane reports a declaration that was lost this way.
