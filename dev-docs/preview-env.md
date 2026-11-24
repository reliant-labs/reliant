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

## Only this env

`internal/forgetest/fixtures_env_test.go` holds this line in three ways:

- it fails on a fixture key, or `forge.Fixtures`, in any other env's KCL source;
- it fails when another env's render carries one;
- it renders this env and fails unless the render serves the shop pack read-only
  with only `.invalid` origins.

Prod's `config.js` is rendered by control-plane's `deploy/kcl`, which these tests
cannot see. That is covered by the post-merge check of the PR that added fixture
mode (#3453).

## Building and deploying

`forge.Fixtures` needs a forge with forge#2719. Until reliant's forge pin
includes it, `forge env render preview` fails to compile the env, and so does
the render test above.

**Nothing deploys this env today.** The release train (control-plane's
`rc-train` / `scripts/cut-release.sh`) deploys control-plane's envs. No
deploy/kcl env of this repo is among them. Where the preview is hosted is an
**open owner decision**. The two options are:

- a frontend in control-plane's prod beside `reliant-web`, built from the same
  pinned source and promoted with prod's release, which is how
  demo.reliantlabs.io ships;
- reliant's own env.

This declaration is what either home would use. Until the owner decides, a chat
never deploys it: no `forge env build preview` and no `forge env deploy preview`.
When it is deployed, `forge obs check preview --wait --json` proves the release.

The platform already has a `preview` env for this project. It is promoted to the
scratch build `v0.0.0-preview.topbar.20261122.1` (`forge env render preview`
prints it) at daring-marten.sites.reliantlabs.dev. That deployment came from the
scratch branch, not from this declaration.

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
