---
name: frontend/scaffolded
description: The systems forge scaffolds into every frontend for you to extend — native auth (useAuth), the typed event bus, the Zustand UI store, format-utils and admin-url — and the dev workflow: forge env up dev-serving each frontend on its KCL-declared PORT.
---

# Scaffolded systems and the dev workflow

## Scaffolded systems (yours to extend)

Created by `forge scaffold frontend`, yours to modify:

- **Auth** (`src/lib/auth/`) — native sign-in: the browser POSTs credentials to this app's own API and gets an HttpOnly session cookie; the server runs the OIDC flow. `useAuth()` gives `{ identity, isAuthenticated, isLoading, refresh, logout }` — an identity for rendering, and deliberately no token. See `auth/frontend`.
- **Event bus** (`src/lib/events.ts`; hooks in `src/lib/use-event.ts`) — typed pub/sub for imperative cross-cutting actions (`toast:show`, `auth:expired`, `navigate`). Extend `EventMap`; use `useEvent(name, handler)`. Not a source of truth.
- **UI store** (`src/stores/ui-store.ts`) — Zustand baseline for client state (`sidebarCollapsed`, `commandPaletteOpen`). Add domain stores in `src/stores/` (rules: `frontend`, "Sub-skills & rules").
- **`src/lib/format-utils.ts`** — see `frontend`, **Formatting**.
- Also scaffolded: `src/lib/admin-url.ts` (`adminUrl`/`absoluteAdminUrl` over `basepath_gen.ts` — use these or `joinBasePath` for any string handed to an external system that round-trips back).

## Dev workflow

```bash
forge env up dev   # infra + services (hot reload) + frontends; reads deploy/kcl/<env>/
forge generate     # After any .proto change
```

`forge env up` dev-serves each declared frontend via `npm run dev` (`npm run build` is for `forge build` / `forge env deploy`). Each serves on its own KCL-declared port (`forge.Frontend.port`), **force-injected as `PORT`** into the child (a stale shell `PORT` never wins). There is no dev reverse proxy. To route a service under the prod Gateway, declare an `HTTPRoute` with a `host:` in `deploy/kcl/<env>/main.k`.
