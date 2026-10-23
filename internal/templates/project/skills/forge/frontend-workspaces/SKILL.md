---
name: frontend-workspaces
description: Opt-in pnpm-workspaces layout for sharing TypeScript code across multiple frontends (web + mobile). When to use it, the resulting layout, how to add new shared packages, and the dev loop.
---

# Frontend Workspaces

## When to use this

The default forge layout gives each frontend its own standalone
`package.json` under `frontends/<name>/`. That works fine for a single
frontend — but the moment you add a second one (e.g. a Next.js web app
plus a React Native Expo app), each frontend ends up with:

- Its own copy of the buf-generated Connect TS clients (`src/gen/`).
- Its own copy of the React Query hook wrappers.
- No clean way to share custom hooks, design tokens, or domain types.

The pnpm-workspaces layout reshapes the project into a pnpm workspace
so all frontends can share these pieces from one source of truth.

Reach for it when:

- You have **two or more frontends** (web + mobile is the canonical
  case).
- You want **one place** for the generated TS clients, hooks, or
  cross-frontend utilities.
- You're OK adopting **pnpm** as the package manager (pnpm is required
  — it's the only manager that handles `workspace:*` resolution
  cleanly).

If you only have one frontend, leave the layout off. The default layout
is simpler.

## Turning it on

Pass `--frontend-workspaces` to `forge project new`. That writes
`pnpm-workspace.yaml` at the project root, and **that file is the switch**:
forge detects the layout from it, so there is no `frontend:` block in
`forge.yaml` to set. To adopt the layout in an existing project, add the root
`pnpm-workspace.yaml` (`packages: ["packages/*", "frontends/*"]`); the next
`forge generate` emits `packages/api` and `packages/hooks` without disturbing
your existing frontend's code. Delete the file to go back to the per-frontend
layout.

## Resulting layout

```
<project root>/
  pnpm-workspace.yaml          # lists packages/* + frontends/*
  packages/
    api/                       # @<project>/api
      package.json
      buf.gen.yaml             # buf emits TS stubs here
      src/
        index.ts               # re-export barrel
        gen/                   # buf-generated *_pb.ts (committed, like all generated code)
    hooks/                     # @<project>/hooks
      package.json
      src/
        transport.ts           # setApiTransport() bootstrap
        use-api-query.ts       # base wrappers (DOM-free)
        use-api-mutation.ts
        index.ts
        generated/             # per-service hooks (forge-generated)
          users-hooks.ts
          orders-hooks.ts
          index.ts
    ui-web/                    # @<project>/ui-web — shared web components
      package.json
      src/
        components/
          ui/                  # React/Tailwind component library
            button.tsx
            card.tsx
            ...
        index.ts               # re-export barrel
  frontends/
    web/                       # Next.js
      package.json             # declares "@<project>/api": "workspace:*"
                               # plus "@<project>/ui-web": "workspace:*"
      tsconfig.json            # paths: "@/components/ui/*" → ui-web
      src/
        lib/connect.ts         # builds Transport, calls setApiTransport()
        ...
    mobile/                    # Expo / React Native
      package.json             # api+hooks deps; NOT ui-web (web-only)
      ...
```

The npm scope is derived from your project name (`forge.yaml: name`).
`name: my-app` produces `@my-app/api` and `@my-app/hooks`. The scope is
lowercased and stripped to valid npm chars; falls back to `@app` if the
name doesn't produce a valid segment.

## Dev loop

After flipping the flag and running `forge generate`:

```bash
# install + link all workspace members in one shot
pnpm install

# run a frontend
pnpm --filter web dev      # Next.js
pnpm --filter mobile start # Expo

# type-check everything (each workspace + each frontend)
pnpm -r typecheck
```

## How the pieces fit together

The `@<project>/hooks` package is **DOM-free** — its `tsconfig.json`
omits the `DOM` lib so accidentally reaching for `document` or
`window` is a compile error. This is what makes the same hooks usable
from Next.js and React Native.

Because the hooks package can't construct its own fetch-based
Transport (that's frontend-specific), it exposes a tiny `transport.ts`
shim:

```typescript
// packages/hooks/src/transport.ts
import { createClient, type Transport } from "@connectrpc/connect";

let _transport: Transport | null = null;

export function setApiTransport(t: Transport) { _transport = t; }
export function connectClient<S>(service: S) {
  return createClient(service, _transport ?? throwIfNull());
}
```

Each frontend builds its Transport (browser fetch in Next.js, custom
fetch in React Native) once at startup and hands it over:

```typescript
// frontends/web/src/lib/connect.ts (auto-generated)
import { setApiTransport } from "@my-app/hooks";

export const transport = buildTransport();
setApiTransport(transport);
```

After that, every hook in `@my-app/hooks/generated` resolves its
Connect client against whichever Transport is registered. The hook
files themselves don't import from frontend-specific paths, so they
work identically in every workspace member.

## The `ui-web` package

`packages/ui-web/` holds the shared React + Tailwind component library
that browser-targeted frontends (Next.js, Vite SPA) import from. In the
non-workspaces layout the same components get copied into EVERY
`frontends/<name>/src/components/ui/` directory. With workspaces on
they live ONCE under `packages/ui-web/src/components/ui/`, and per-
frontend tsconfig path mapping redirects the existing
`@/components/ui/*` imports to the shared package.

The mapping is scoped to `/ui/` deliberately — non-ui local paths
like the owned `@/components/nav.tsx` and `@/components/session_nav.tsx`
keep resolving against the per-frontend `src/` tree, so the components
each frontend owns keep working unchanged.

### Ownership rule

forge writes `packages/ui-web/` files **once**, on first scaffold.
After that re-running `forge generate` is a no-op for every file under
the package — the user owns them. Edit, restyle, delete, rename
freely; nothing here will be clobbered.

`src/index.ts` (the re-export barrel) is seeded once from the
components directory and then owned by you too. Add or remove
`export { default as ... }` lines as you grow the library — forge
won't fight you.

### How frontends import from it

Templates keep their existing `@/components/ui/...` import sites
unchanged — no rewrite required when flipping workspaces on. The
redirection happens at the tsconfig + bundler level:

```jsonc
// frontends/web/tsconfig.json (workspaces mode)
"paths": {
  "@/components/ui/*": ["../../packages/ui-web/src/components/ui/*"],
  "@/*": ["./src/*"]
}
```

For Vite frontends the same alias is mirrored in `vite.config.ts` so
the bundler resolves to the same paths the type-checker sees.

Next.js reads `tsconfig.json` paths directly through its webpack
config — no extra wiring needed.

### Adding a new component

```bash
# 1. write the component file:
$EDITOR packages/ui-web/src/components/ui/my_widget.tsx

# 2. expose it from the barrel:
$EDITOR packages/ui-web/src/index.ts
#   add: export { default as MyWidget } from "./components/ui/my_widget";
```

Then in a frontend:

```typescript
import MyWidget from "@/components/ui/my_widget";    // path-mapped, default
// or
import { MyWidget } from "@<scope>/ui-web";          // named barrel export
```

### React Native does NOT consume `ui-web`

`packages/ui-web/` is web-targeted (DOM lib enabled, Tailwind
utility classes). React Native frontends use platform-specific
primitives and don't depend on the package. The forge generator
skips the workspace dep when scaffolding RN frontends.

### Why this isn't on npm

`packages/ui-web/` is project-specific scaffolding, not a shared
library. forge seeds a starting set (button, card, data table, etc.)
and gets out of the way — components are yours to fork, restyle, or
rewrite to match your product. Publishing to npm would defeat the
point of giving every project its own forkable design system.

## Adding a new shared package

You can add your own shared packages alongside the forge-generated
`packages/api/`, `packages/hooks/`, and `packages/ui-web/`. Common
examples:

- `packages/types/` — shared TypeScript types not derived from protos.
- `packages/utils/` — shared helpers.
- `packages/ui-mobile/` — if you want a React Native primitives layer
  paralleling `ui-web`.

```bash
mkdir packages/ui
cd packages/ui
pnpm init
# edit package.json:
#   "name": "@my-app/ui"
#   "main": "./src/index.ts"
```

Then in a frontend:

```bash
cd frontends/web
pnpm add @my-app/ui   # resolves to workspace:*
```

Forge will never touch your own packages — only `packages/api/` and
`packages/hooks/` are managed.

## What forge owns vs you

| Path | Owner | Regenerated by `forge generate`? |
| ---- | ----- | -------------------------------- |
| `pnpm-workspace.yaml` | forge (initial), you (after) | only if missing |
| `packages/api/package.json` | forge (initial), you (after) | only if missing |
| `packages/api/src/index.ts` | you | only if missing |
| `packages/api/src/gen/` | forge | every run (buf) |
| `packages/hooks/package.json` | forge (initial), you (after) | only if missing |
| `packages/hooks/src/use-api-*.ts` | forge (initial), you (after) | only if missing |
| `packages/hooks/src/generated/` | forge | every run |
| `packages/ui-web/package.json` | forge (initial), you (after) | only if missing |
| `packages/ui-web/tsconfig.json` | forge (initial), you (after) | only if missing |
| `packages/ui-web/src/components/ui/*.tsx` | forge (initial), you (after) | only if missing |
| `packages/ui-web/src/index.ts` | forge (initial), you (after) | only if missing |
| `frontends/<name>/src/lib/connect.ts` | forge (initial), you (after) | only if missing |

Rule of thumb: the `src/gen/` and `src/generated/` directories are
regenerated every cycle. Everything else is yours to edit once it
lands.

## Why pnpm only

- `workspace:*` is a pnpm-specific protocol. npm and yarn classic use
  different conventions that don't compose with React Native's Metro
  bundler.
- pnpm's content-addressable store means N frontends sharing N copies
  of `react-native` cost the disk space of one copy.
- forge doesn't generate a `package-lock.json` or `yarn.lock`; only
  `pnpm-lock.yaml` is supported in workspace mode.

## See also

- `frontend` skill — single-frontend dev loop, component library,
  React Query patterns.
- `proto` skill — what changes in `proto/services/<svc>/v1/` flow
  through buf into `packages/api/src/gen/`.
