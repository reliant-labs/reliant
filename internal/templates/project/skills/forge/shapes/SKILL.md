---
name: shapes
description: Find project symbols across proto, schema, handlers, hooks and stores with forge project shapes; inspect library APIs without hunting generator source.
---

# Project reconnaissance

From the start-here skill: use this before hunting symbols across layers.

### `forge project shapes` — start recon here, not with `grep`

**Use it the moment you need to know where something is or what shape it has.**
It answers, in ONE call, the question that otherwise becomes a sequence of
greps: *where is this symbol, in every layer, and what is its signature?*

```bash
forge project shapes                      # every symbol in the project
forge project shapes --grep Estimate      # one entity across ALL layers at once
forge project shapes --kind rpc,handler   # what is DECLARED vs what is IMPLEMENTED
forge project shapes --unimplemented      # declared rpcs with no handler, or only a stub
forge project shapes --service Chat       # one service's rpcs and handlers
```

Every line carries `file:line`, so the next step is a targeted read — never
another search:

```
rpc      RoofopsService.ListMaterials  proto/services/roofops/v1/roofops.proto:368  in=ListMaterialsRequest out=ListMaterialsResponse
message  Material                      proto/services/roofops/v1/roofops.proto:737
table    materials                     db/migrations/00010_create_materials.up.sql:9
handler  RoofopsService.ListMaterials  internal/handlers/roofops/handlers_crud.go:790
hook     useListMaterials              frontends/dashboard/src/hooks/roofops-service-hooks_gen.ts:212
```

Reach for it when you would otherwise:

- hunt for a file — `--grep <Name>` names it and the line
- open a large proto to find one message — the line number takes you straight there
- ask "is this RPC implemented yet?" — `--unimplemented` lists every declared rpc with no handler or an `unwired-stub` (handler lines are named like their rpc, and list rpc handlers only)
- ask "does a hook exist for this?" — `--kind hook`

It parses the protos, migrations and source **live on every call** (~2s on a
15-entity project). It deliberately does NOT read `gen/forge_descriptor.json`:
that file is a derived cache, and a recon answer that is confidently wrong is
worse than no answer.

**Never search the filesystem for forge's own source.** Name the package and
`forge project libraries` prints its full exported API — every signature, no
file reading:

```
forge project libraries svcerr             the whole package
forge project libraries crud orm svcerr    three packages, one call
forge project libraries orm.Context        one type, with its methods
```

Reach for that rather than `go doc <pkg>`, which collapses every struct and
interface to `struct{ ... }` and lists no methods — it cannot tell you
`crud.Repo.UpdateMasked`'s parameters. (For your OWN generated interfaces,
`go doc ./internal/db Store` is fine: `go doc` renders interfaces in full.)
