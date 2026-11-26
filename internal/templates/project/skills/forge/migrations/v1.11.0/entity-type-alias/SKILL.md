---
name: migrations/v1.11.0/entity-type-alias
description: Generated pages import the entity type under an alias (forge v1.11.0), `import type { Link as LinkEntity }`, so an entity named like anything a page imports for itself — next/link's Link, Image, Head, Script, Form, @tanstack/react-router's Link, the component library's Button, Input or Resource — no longer declares that identifier twice and fails the frontend's typecheck. Pages are scaffold-once, so a page written by an older forge keeps the colliding import. `forge project upgrade` rewrites each page that has the collision (`forge fix entity-type-alias`): the type import gets the alias and its uses as a type argument or array type are renamed; a use it cannot classify is listed with its file:line. Use when a frontend's typecheck fails with "Duplicate identifier" on an entity name, or a page imports an entity type from a `_pb` module under a name another import of the file also binds.
version: v1.11.0
automation: fix:entity-type-alias
detection: forge-detect entity-type-alias
---

# Entity type imports never collide (v1.11.0)

A scaffolded list page types its columns with the entity's protobuf-es message
type. Before v1.11.0 it imported that type under the entity's own name:

```tsx
import Link from "next/link";
// …
import type { Link } from "@/gen/services/links/v1/links_pb";
```

An entity named `Link` therefore declared `Link` twice, and `tsc` (and
`forge lint`'s frontend lane) failed with `Duplicate identifier 'Link'` on a
project that had just been scaffolded. Every name a generated page imports for
itself collides the same way: `Image`, `Head`, `Script` and `Form` from next/*,
`Link` from `@tanstack/react-router` in a Vite frontend, `Resource` from the
runtime, `Button`, `Input`, `FilterBar` from the component library, `Plus` from
lucide-react.

## What changed in forge

Every generated page binds the entity type under a suffixed local name:

```tsx
import type { Link as LinkEntity } from "@/gen/services/links/v1/links_pb";

const columns: ResourceColumn<LinkEntity>[] = [ … ];
// …
<Resource<LinkEntity> … />
```

The suffix is always `Entity`, for every entity, so the name is predictable and
no list of reserved names is needed. The proto message, its RPCs and hooks keep
their names.

## What does not change by itself

The pages under `src/app/<entity>/` (Next.js) and `src/pages/<entity>/` (Vite)
are scaffolded once and are yours: moving `forge_version` rewrites none of them.
A page that never collided keeps `import type { Task }`, which is still correct.

`forge project upgrade --to v1.11.0` (or later) moves the pin and then runs this
playbook's automation, `forge fix entity-type-alias`. To run it on its own:

```bash
forge fix entity-type-alias --dry-run   # each page it would rewrite, and each use to check
forge fix entity-type-alias
git diff
cd frontends/<name> && npx tsc --noEmit
```

It reads every `.ts`/`.tsx` under each frontend's `src/` (the project root and
every `frontends/<name>`; `gen/`, `node_modules/` and `*_gen.*` files are
skipped) and edits only a file with the collision: a type-only import from a
protobuf-es module (`…_pb`) binding a name that another import of the same file
also binds. In such a file:

1. the specifier becomes `X as XEntity`;
2. each use of `X` as a type argument (`ResourceColumn<X>`, `<Resource<X>`,
   `Foo<X, Y>`, `Foo<A, X>`) or as an array type (`X[]`) becomes `XEntity`.
   JSX (`<X href=…>`, `</X>`) belongs to the other import and stays.

A file whose alias is already bound by something else is not edited; the run
names it. Re-running the codemod changes nothing: a repaired file no longer has
the collision.

## What it leaves to you

Every remaining reference to `X` outside imports and comment-only lines is
listed as `file:line` with the line's text. This is intentionally conservative:
the codemod is not a TypeScript parser. Annotations (`(item: Link) =>`), type
aliases (`type X = Link`), unions, tuples and nested generics may need manual
repair. Values and JSX are also listed; their presence does not mean they
should be renamed. If a reference means the entity type, write `XEntity`; if it
means the component, leave it. Resolve this report from the first run: after
the import is aliased a second run no longer detects a collision and does not
repeat the guidance. A rewrite alone is not proof of a completed repair; run
`npx tsc --noEmit` in the frontend to verify it.

A page you renamed or restructured so that its type import no longer comes from
a `_pb` module is outside what the codemod reads; give that import an alias by
hand the same way.

## Enums

New pages bind protobuf-es enums the same way, with the suffix `Enum`:
`import { Button as ButtonEnum } from "…_pb"`, and every reference on the
page (`ButtonEnum.PRIMARY`, `enumType={ ButtonEnum }`, the zod pipe) uses it.
An enum is imported as a value, not `import type`, so the codemod does not
touch it. An older page whose enum shares a name with another import (tsc:
`Duplicate identifier` on the enum's name) is fixed by hand: alias the
specifier and rename the page's uses of the enum.
