---
name: adopt
description: Generating into a frontend forge did not scaffold — an existing app moved under frontends/ or named by a KCL path. What forge writes and withholds, one writer per src/gen, hand-written barrels and case collisions, and the package.json "forge" block (routes, hooks, components_dir).
---

# Adopting an existing frontend

An app forge did not scaffold — moved under `frontends/<name>/`, or named by a
KCL `forge.Frontend` `path` — is **adopted**. forge tells the two apart by
record, not guesswork: it scaffolded a frontend when `.forge/scaffolded.json`
records its `package.json`, or its directory holds the nested `go.mod`
boundary marker forge writes. Anything else is adopted, and `forge generate`
writes only what generated code needs into it:

| | scaffolded frontend | adopted frontend |
|---|---|---|
| Tier-1 codegen (hooks, mocks, `apiurl_gen.ts`, `otel_gen.ts`, typed config) | yes | yes |
| owned seams the hooks import (`src/lib/connect.ts` + `api-base.ts`, `src/lib/test-utils.tsx`) | came with the scaffold | scaffolded once if missing; the app's own module is used when present |
| starter components in `src/components/ui/` | yes | no — `forge component install <name>` |
| CRUD pages, nav, dashboard, home page | yes | no, unless `routes` asks for them |
| `package.json` dependency edits | reconciled | reported, never written |

Rules that hold for every frontend, adopted or not:

- **One writer per `src/gen`.** A root `buf.gen*.yaml` plugin whose `out:` is
  the frontend's `src/gen` makes its TypeScript stubs project-owned: your plugin
  pin and options generate them and forge writes no second copy. That template
  plus the frontend's own `buf.gen.yaml` is two writers, and generate refuses.
  Stubs from a generator no template declares are left alone and named.
- **A hand-written `src/hooks/index.ts` is never overwritten.** forge writes no
  barrel over it and names it in the summary; import generated hooks from
  their `*-service-hooks_gen` modules, or `forge project disown` the path.
- **No component is written beside one whose name differs only in case**
  (`button.tsx` next to `Button.tsx`): that is one file on macOS.

Per-frontend codegen knobs live in the frontend's own `package.json` — no
deploy env needed:

```json
"forge": {
  "routes": ["none"],
  "hooks": ["ChatService", "ProjectService"],
  "components_dir": "src/components/forge-ui"
}
```

`routes` is the CRUD-page allowlist (route slugs, or `none`; a KCL
`forge.Frontend.routes` wins when it states one; an adopted frontend defaults
to `none`). `hooks` lists the proto services that get hooks (short or
fully-qualified names; absent = all, `[]` = none). `components_dir` is where
forge's component library lives, relative to the frontend. An unknown key, or a
`components_dir` outside the frontend, fails generate rather than being ignored.

Route every service your app does not send to the default origin in
`connectClient()` (`src/lib/connect.ts` shows how): the generated hooks only
ever call that function.
