---
name: adopt
description: Generating into a frontend forge did not scaffold — an existing app moved under frontends/ or named by a KCL path. What forge writes and withholds, one writer per src/gen, hand-written barrels and case collisions, and the package.json "forge" block (routes, hooks, components_dir).
---

# Adopting an existing frontend

An app forge did not scaffold — moved under `frontends/<name>/`, or named by a
KCL `forge.Frontend` `path` — is **adopted**. forge tells the two apart by
record, not guesswork: it scaffolded a frontend when `.forge/scaffolded/`
records its `package.json`, or its directory holds the nested `go.mod`
boundary marker forge writes. Anything else is adopted, and `forge generate`
writes only what generated code needs into it:

| | scaffolded frontend | adopted frontend |
|---|---|---|
| Tier-1 codegen (hooks, mocks, `apiurl_gen.ts`, `otel_gen.ts`, typed config) | yes | yes |
| owned seams the hooks import (`src/lib/connect.ts` + `api-base.ts`, `src/lib/test-utils.tsx`) | came with the scaffold | scaffolded once if missing; the app's own module is used when present |
| starter components in `src/components/ui/` | yes | no — `forge component install <name>` |
| CRUD pages, nav, dashboard, home page | yes | no, unless `routes` asks for them |
| `package.json` dependency edits (runtime floor, telemetry SDK) + lockfile refresh | yes | yes |

The dependency edits only raise `@reliantlabs/forge-web-runtime` to the floor
the regenerated code needs (never lower a newer range) and declare what
generated code imports. `forge project disown <frontend>/package.json` stops
them; generate then prints each change instead. A later generate that finds
the tree unchanged says `nothing to do` and does not print them again (it
would write nothing); `forge generate --no-cache` repeats the report.

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
- **`tsc` must type-check the tests**, including the generated
  `*-service-hooks_gen.test.tsx`. A scaffold keeps one project whose `include`
  covers them. Vite's own template does not: its `tsconfig.app.json` excludes
  every test and nothing else includes them, and vitest strips types without
  checking them, so a test importing a removed export passes both. An adopted
  app with that split adds a `tsconfig.test.json` that holds **only the tests**
  and references the app project, and lists it in the root `tsconfig.json`'s
  `references`:

  ```json
  {
    "extends": "./tsconfig.app.json",
    "compilerOptions": {
      "tsBuildInfoFile": "./node_modules/.tmp/tsconfig.test.tsbuildinfo",
      "composite": false,
      "incremental": true,
      "declaration": false,
      "emitDeclarationOnly": false,
      "noEmit": true,
      "types": ["vitest/globals", "@testing-library/jest-dom"]
    },
    "references": [{ "path": "./tsconfig.app.json" }],
    "include": ["src/**/__tests__/**/*", "src/**/*.test.*", "src/**/*.spec.*", "src/test/**/*", "src/**/*.d.ts"],
    "exclude": ["src/gen/**/*"]
  }
  ```

  The app project, in place of its `"noEmit": true`, becomes `composite` and
  emits declarations only, out of the tree:

  ```json
  {
    "compilerOptions": {
      "composite": true,
      "emitDeclarationOnly": true,
      "outDir": "./node_modules/.tmp/tsc"
    }
  }
  ```

  Do not give the test project `"include": ["src"]`: every app file is then
  type-checked twice, once per project, which on reliant's web app made
  `tsc -b` the slowest step of its merge gate. With the reference, `tsc -b`
  checks the app once and the tests read its emitted `.d.ts`; an app edit that
  leaves the declarations alone does not re-check the tests. `src/**/*.d.ts`
  keeps the app's global declarations (`vite/client`, `Window` augmentations)
  in scope, because a referenced project's own `.d.ts` files are not emitted.
  `incremental` keeps per-file results in the build info; `composite: false`
  turns it off, and without it `tsc -b` re-checks every test whenever one
  changed.
  A project the app itself references (a separate one for `src/gen`) emits
  into the same `outDir`, so the relative imports between them in emitted
  `.d.ts` still resolve: under `skipLibCheck` one that does not types as `any`
  in the tests, silently. Declaration emit can also fail an export whose
  inferred type names a type its module does not export (TS4023, TS2742):
  export that type, or annotate the export. `types` names whatever globals the
  test setup declares; omit it when tests import `vitest` explicitly.

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
