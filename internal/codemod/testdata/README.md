`legacy/` is a project forge scaffolded before the composition graph (forge
357579470b0d, `project new app --service tasks`, a `Task` entity and a `ticker`
worker), trimmed to the files `forge fix composition-graph` reads. Its go.mod is
`go.mod.txt` so the forge module never sees a nested module; the tests copy the
tree into a temp dir and restore the name.

`entitytypealias/` holds the list pages `forge generate` writes for an entity
named `Link` (served by all five CRUD rpcs; `crudPageForEntity` in
internal/codegen): `*.before.tsx` is the shape forge wrote before v1.11.0 (the
entity type imported under its own name, colliding with `Link` from next/link
or @tanstack/react-router), `*.after.tsx` is today's. `forge fix
entity-type-alias` must turn each before into its after, byte for byte, and
internal/codegen's TestListPage_RendersWhatTheEntityTypeAliasCodemodWrites holds
the afters equal to what the templates render.
