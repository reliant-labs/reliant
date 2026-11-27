# The preview env: the production web build on shop fixtures

`deploy/kcl/preview/main.k` declares a hosted, clickable preview of the web
app. Visitors see the shop world, signed in as its user, and nothing they do
reaches a backend.

## What it is

It is one forge frontend, `web`: `frontends/web` built by the plain
`npm run build`, with no env_vars, no build target and no harness plugin. The
`dist/` is the same one the visual harness serves and app.reliantlabs.io would
get. Only the env's `config.js` differs:

- **`fixtures = forge.Fixtures { scenario = "shop" }`** renders
  `FIXTURES_SCENARIO = "shop"` and `FIXTURES_READ_ONLY = "true"` (forge#2719).
  `src/lib/fixturesConfig.ts` reads them. When they are present, `main.tsx` waits
  for `src/lib/fixturesBoot.ts`, which lazily loads `src/fixtures/install.ts`
  before the first render:
  - every Connect RPC is answered in the page by the shop pack
    (`src/fixtures/shop/app.ts`, which extends `shopPack`);
  - the answers pass through the app's real interceptors (`installFixtureTransport`
    in `lib/connect.ts`): auth, the org header and error normalization;
  - every write is refused.
- **The app's own keys** are typed by `ReliantWebConfig`, with
  `AUTH_MODE = "cloud"` so the managed code path runs. Every origin is a fake one
  under `.invalid` (RFC 6761: it never resolves).
- **The fetch boundary** is `src/fixtures/outside.ts`. It answers the configured
  Supabase origin's `/auth/v1/*` with a fixed session for the pack's user, which
  it writes to localStorage before the Supabase client exists. It returns 204 for
  telemetry sinks, and throws on an RPC that escaped the transport or on any other
  request off the page.

A config.js without the keys loads none of this. The entry-chunk guard
(`vite-plugin-fixture-entry-guard.ts`) keeps fixture code out of the app's entry
chunks.

## The same pack as the harness

The visual harness (`frontends/web/e2e/visual`) serves the same `dist/` with the
same two keys in its `config.js`. It also passes `window.__forgeFixtures.now`
(FIXED_NOW) to pin the clock, and a list of overlays. The preview passes neither,
so its clock is the page load and the story's times move with it. Otherwise a
harness screenshot shows what a preview visitor sees.

- **Overlays:** `?fixtures=<name>[,<name>]` applies the overlays registered in
  `src/fixtures/shop/overlays.ts` (for example `?fixtures=no-daemons`). The list
  is allow-listed by name, and the parameter is read only in fixture mode.
- **"Ask Reliant":** it stays in its chat mode, so the preview shows the buttons
  live Reliant shows. Starting a chat is a write, and the read-only guard refuses
  it.

## Only this env, and only inert

The preview serves prod's own build, so **the real sign-in and API code is in
it. The guarantee is that this code is present but provably inert**: it has
nothing live to reach, and nothing it does leaves the page. These tests prove
it:

- **`internal/forgetest/fixtures_env_test.go`**:
  - it fails on a fixture key, or `forge.Fixtures`, in any other env's KCL source;
  - it fails when another env's render carries one;
  - `TestThePreviewEnvRendersFixtureMode` renders this env and asserts the shop
    scenario, read-only, every origin (each `*_URL` key and `OTEL_ENDPOINT`)
    empty or under `.invalid`, and no real key (no Stripe key, no Supabase
    key shaped like a real one, no replay). It also holds the render equal to
    `deploy/kcl/preview/runtime_config.golden.json`, the config.js the browser
    test serves (`UPDATE_PREVIEW_CONFIG=1` rewrites it).
- **`deploy/kcl/fixtures_guard.k`** refuses the render when a frontend in
  fixture mode (forge's `fixtures`, or the key set by hand) names any origin
  that is neither empty nor under `.invalid`. A `WorkloadURL` counts as live.
  This env renders through it, and control-plane's prod frontend will import
  it (`import reliant.fixtures_guard`). `internal/forgetest/fixtures_guard_test.go`
  tests it both ways, over `deploy/kcl/testdata/fixtures_guard_*.k`.
- **`frontends/web/e2e/visual/preview-inert.visual.ts`** (`@harness`, no
  screenshots) runs `vite preview` of `dist/` with that exact config.js, on a
  fresh profile. On each surface it aborts and records every request off the
  page origin (fetch, XHR, images, WebSocket, sendBeacon) and asserts there are
  none. It also asserts that `/auth/v1/*` is answered in the page with the
  fixture session, and that the page is noindex. Then it makes real writes
  through the UI (sending a chat message from the home composer, and two
  settings saves) and asserts that the read-only guard refuses each one, again
  with nothing sent.

  Run it with:

  ```sh
  pnpm build && BASE_URL=http://127.0.0.1:<port> npx playwright test -c playwright.visual.config.ts preview-inert --project desktop-dark
  ```

  with `vite preview --port <port>` running.

Prod's `config.js` is rendered by control-plane's `deploy/kcl`, which these
tests cannot see. The post-merge check of #3453 covers it.

The longer-term answer is a bundle with no sign-in code at all, built for the
preview after phase 3's session port:
https://github.com/reliant-labs/reliant/issues/3459. The public demo
(demo.reliantlabs.io, `WEB_BUILD_TARGET=demo`) is that kind of bundle, and it now
shows the same app surfaces on the same shop data (chats, Inbox, Workflows,
Connectors, Sandboxes, the app's nav, "Acme · Starter"), as a fixed signed-in visitor
with no token. Its build guard (`vite-plugin-demo-bundle.ts` `DEMO_FORBIDDEN`) fails the
build if Supabase, `lib/auth`, the auth store, billing or the Settings page's account
sections reach it, so unlike this env its inertness is a property of the bundle, not
of a config. It serves the app layer (`fixtures/shop/app.ts`) without fixture mode's
install or fake Supabase. See `demo-inventory.md`, "The bundle guard". This env stays
as the check that prod's own build, sign-in code included, is inert on fixtures.

## Not indexed

`index.html` is shared with app.reliantlabs.io, which must stay indexable, and a
hosted site cannot set an `X-Robots-Tag` header. So fixture mode adds
`<meta name="robots" content="noindex, nofollow">` at runtime
(`src/fixtures/noindex.ts`, run first by `install.ts`). A live page never loads
it. Crawlers that run JavaScript honour the tag. One that reads only the raw
HTML sees prod's empty shell. A header would cover it too, but that needs
control-plane's static origin to set it per site, which is a separate feature.

## Building and deploying

`forge.Fixtures` needs a forge with forge#2719. Until reliant's forge pin
includes it, `forge env render preview` fails to compile the env, and so do
the tests above that render it.

**Where it is hosted (owner decision, option A):** the preview is a frontend
in **control-plane's prod env**, beside `reliant-web` and `forge-demo`:

- built from the same pinned reliant source;
- promoted by digest with every prod release (the release train, rc-train,
  deploys it; no chat ever does);
- served on a platform-assigned `*.sites.reliantlabs.dev` hostname, noindex.

It is a second hosted site on the org's billing. The control-plane PR that
declares it follows once forge#2719 and the pin bumps land. That PR copies this
env's frontend and applies `reliant.fixtures_guard`'s `inert_frontend` to it.

This env stays as the declaration that frontend copies, the subject of the
tests above, and a local target. It is not deployed from here. A custom domain
(e.g. preview.reliantlabs.io) is an optional owner follow-up.

The platform still has a `preview` env for this project, promoted to the
scratch build `v0.0.0-preview.topbar.20261122.1` (`forge env render preview`
prints it) at daring-marten.sites.reliantlabs.dev. That deployment came from
the scratch branch. Retire it once the control-plane frontend serves.

## The scratch branch is retired

The hosted preview first ran from `preview/scratch-commit` (1dc4712a), which was
never merged. It had:

- a `WEB_PREVIEW_HARNESS` Vite plugin, a `Buffer` global and a `Date` shim;
- an in-browser FixtureServer that replayed the visual harness's fixtures;
- about 8k lines of preview-only observability data;
- a second frontend, `web-preview`.

All of it is superseded. The preview is now the production build plus runtime
config, and the shop pack is its one data source. Nothing from that branch
reaches main. Retire it once this env serves the preview.
