---
name: migrations/v0.1.46/dev-api-proxy
description: Adopt the same-origin dev API proxy — under the dev server a web frontend's browser reaches the API at `<basePath>/_api` on its own origin (a `next.config.ts` rewrite / `vite.config.ts` proxy), chosen by a new `src/lib/api-base.ts` that `connect.ts` and `auth/native-login.ts` both ask. Use when a Next.js or Vite frontend's `next.config.ts` / `vite.config.ts` has no `FORGE_DEV_API_PROXY`, or when the app works on localhost but every RPC and sign-in fails through a Reliant workspace preview URL, a tunnel or a LAN IP.
---

# Same-origin dev API proxy (v0.1.46, section 5 of `migrations/v0.1.46`)

Use when a Next.js or Vite frontend's `next.config.ts` / `vite.config.ts` has
no `FORGE_DEV_API_PROXY`, or when the dev app works on localhost but signing
in and every RPC fail once the dev server is opened any other way: a Reliant
workspace preview URL (`https://<port>-<id>.workspaces.reliantapi.com`), a
tunnel to the frontend port, a LAN or container IP.

## What changed

- **The cause.** `forge env up` hands the dev server the API's URL
  (`NEXT_PUBLIC_API_URL` / `VITE_API_URL`, `http://localhost:<api port>`), and
  the dev `config.js` names the same. The browser called it directly. That is
  the VIEWER's localhost whenever the browser is not on the machine running
  the stack, so every RPC and `POST /auth/login` failed (Private Network
  Access blocks a public page calling a loopback address regardless).
- **Now:** under the dev server the browser calls `<origin><basePath>/_api/*`
  and the dev server forwards it to the API — the same pattern as the
  existing `/_otel` telemetry proxy. The session cookie the API sets
  (`Path=/`, no `Domain`) becomes first-party to the page's host, and CORS is
  not involved in dev.
- **Builds are unchanged.** A static export (or any production build) has no
  rewrites; it reads `API_URL` from the runtime `config.js` exactly as before.
- **Opt out** by starting the dev server with `FORGE_DEV_API_PROXY=off`. The
  proxy is also off when the dev server has no upstream (no
  `NEXT_PUBLIC_API_URL` / `VITE_API_URL`). An explicit API URL from your shell
  or KCL `env_vars` still wins over forge's: it becomes the proxy's upstream.
- **Expo** is out of scope: a native app has no page origin to proxy through.
- `forge lint`'s `forgeconv-frontend-process-env` treats `FORGE_DEV_API_PROXY`
  like `NODE_ENV` — a value the frontend's own bundler config defines, not
  deployment config. On a forge older than this, the read in `api-base.ts`
  is a warning.

All of these files are scaffold-once ("yours"), so nothing arrives by
itself. `forge project upgrade --check --all` lists each one with its diff:
`src/lib/api-base.ts` and its test as absent, and `next.config.ts` /
`vite.config.ts`, `src/lib/connect.ts` and `src/lib/auth/native-login.ts` as
behind (or diverged, where you have edited them). No generated (`*_gen.*`)
file changes, so a project on an older forge can adopt this by hand.

## Steps (Next.js)

Paths are relative to the frontend, e.g. `frontends/web/`.

1. **`src/lib/api-base.ts` and `src/lib/api-base.test.ts`:**
   `forge project rescaffold frontends/<name>/src/lib/api-base.ts frontends/<name>/src/lib/api-base.test.ts`.
   The module exports `apiBase(configured)`: `<origin><basePath>/_api` while
   `process.env.NODE_ENV === "development"` and the inlined
   `process.env.FORGE_DEV_API_PROXY` is non-empty, otherwise `configured`
   without a trailing slash.
2. **`next.config.ts`:** an unedited copy takes `forge project upgrade --force
   frontends/<name>/next.config.ts` (it prints the diff first). An edited one
   takes three hand edits, beside the existing `/_otel` block:

   ```ts
   const apiUpstream = (process.env.NEXT_PUBLIC_API_URL || "").replace(/\/+$/, "");
   const devApiProxy =
     process.env.NODE_ENV === "development" &&
     apiUpstream !== "" &&
     !/^(off|false|0|no)$/i.test(process.env.FORGE_DEV_API_PROXY ?? "")
       ? "/_api"
       : "";
   ```

   In the config object, `env: { FORGE_DEV_API_PROXY: devApiProxy },` and, in
   the dev-gated `rewrites()` beside `/_otel`:

   ```ts
   ...(devApiProxy
     ? [{ source: `${devApiProxy}/:path*`, destination: `${apiUpstream}/:path*` }]
     : []),
   ```

   Keep the rewrite inside the `process.env.NODE_ENV === "development"` gate:
   `forge lint --static-export` reads that gate.

   Then let the page hydrate: `next dev` refuses its HMR socket and dev
   resources to any origin but localhost unless it is in `allowedDevOrigins`,
   and a page it refuses renders but never hydrates (the sign-in button
   submits the form as a plain GET). Copy `devOrigins()` from a fresh scaffold
   (`import os from "node:os"`; it allows the Reliant preview host from
   `RELIANT_PREVIEW_URL_TEMPLATE` + `PORT`, this machine's own IPv4 addresses,
   and `FORGE_DEV_ALLOWED_ORIGINS`), and add
   `...(process.env.NODE_ENV === "development" ? { allowedDevOrigins: devOrigins() } : {}),`.
3. **`src/lib/connect.ts`:** `import { apiBase } from "./api-base";`, and wrap
   the configured URL: `return apiBase(loadConfig().API_URL || DEV_API_URL);`
   (env-var variant: `apiBase(process.env.NEXT_PUBLIC_API_URL ?? DEV_API_URL)`).
4. **`src/lib/auth/native-login.ts`:** `import { apiBase } from "@/lib/api-base";`,
   and make `apiOrigin()` return `apiBase(String(loadConfig().API_URL || ""))`.
   Any other hand-written `fetch` to the API goes through `apiBase` the same
   way.

## Steps (Vite)

Same shape: rescaffold `src/lib/api-base.ts` (+ test); in `vite.config.ts`
compute `API_UPSTREAM` from `process.env.VITE_API_URL` and `DEV_API_PROXY` as
above, take `({ command, mode })` in `defineConfig`, set
`define: { "import.meta.env.FORGE_DEV_API_PROXY": JSON.stringify(devApiProxy) }`
only when `command === "serve" && mode !== "test"`, and add a
`server.proxy[devApiProxy]` entry (`target: API_UPSTREAM`, `changeOrigin: true`,
strip the prefix). Vite rejects an unknown `Host` outright, so also copy
`devAllowedHosts()` and set `server.allowedHosts: devAllowedHosts(5173)`.
Then steps 3 and 4.

## Verify

1. `forge env up dev -- -H 0.0.0.0` (the flag lets a non-loopback origin reach
   the dev server; a Reliant preview URL does not need it).
2. Open the frontend at a non-loopback origin — `reliant preview-url <port>`,
   or `http://<container ip>:<port>` — sign in, and load a list page.
3. In the browser's network panel every API request is
   `<that origin><basePath>/_api/...` and succeeds; nothing names
   `localhost:<api port>`.
4. `npm test -- src/lib/api-base.test.ts` passes, and `forge lint` is clean.
