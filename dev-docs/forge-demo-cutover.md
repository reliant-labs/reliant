# demo.reliantlabs.io → its own env: the cutover runbook

Run every command from a reliant checkout of `main` that contains `deploy/kcl/demo`, with the
tenant-zero credential. Until step 3 passes its checks, demo.reliantlabs.io stays on
control-plane `prod/reliant-web`: the new env is built, deployed and checked on its own
`*.sites.reliantlabs.dev` URL first, and the domain moves only after that.

Use `forge`, which runs the forge version reliant's forge.yaml pins (`v1.0.17-0.20261105225243-7ad8943ce387`). `reliant forge` embeds v1.0.16 and warns about the version skew.

## Current entry experience

The public demo now leads bare `/` visits to `/forge/demo?section=built`: a visual
build story, the real recorded Forge run, and explicit guided-tour/self-exploration
choices. `/forge/p/shop` opens the running sample console directly; individual-page
deep links remain direct. Contextual “How Forge builds this” help preserves the
current page. The smoke script checks this entry-to-console path before nav clicks.
The cutover steps below describe the original hosting migration.

## What changes

| | Before | After |
|---|---|---|
| Serves demo.reliantlabs.io | control-plane project, env `prod`, frontend `reliant-web` (the whole Reliant app; the host picked the shell) | reliant project, env `demo`, frontend `forge-demo` (the forge-only bundle `frontends/web/demo.html`) |
| Built from | control-plane's reliant pin, shipped by the release train | reliant `main`, whenever you deploy it |
| Runtime config | prod's (API URLs, Supabase, auth mode) | `DEMO_DATA_SOURCE=fixtures`, `DEMO_DATA_URL=""`, `DEMO_TELEMETRY=replay`, `OTEL_ENDPOINT=/_otel`, `OTEL_REPLAY=true` |
| Backend calls | the old prod build calls the API → CORS errors | none: every RPC is answered in the browser |

**Why reliant's project and not control-plane's:**

- The demo builds from the checkout it deploys (`path`, no GitSource pin), so it ships on its own schedule.
- A second reliant pin in control-plane would have to move with go.mod's reliant require (`check-forge-coherence.sh`, `pin-drift.sh`). That would tie the demo to prod's release train.
- The demo calls no control-plane API, so nothing ties it to prod.

**Billing:**

- The free tier is one hosted site per org, counted across envs. The demo env would be the org's fourth site.
- No action is needed. The tenant-zero org `55949942-3ee9-4cfc-bb7c-817bdba75186` is hosting-exempt (`control-plane/internal/plansconfig/grants.prod.yaml`). The exemption resolves to a plan, and the static free-tier count does not apply to orgs with a plan.

## Order

1. Merge the PRs below, in this order:
   - reliant #2334 `feat/demo-ux` (the UX agent's: data, toast, tips, write gating);
   - reliant #2335 `feat/forge-demo-bundle` (the bundle, the guard and `deploy/kcl/demo`);
   - forge#1876 whenever you like (independent).
2. Build, deploy and check the env. The demo env already exists on the control plane:
   - A build attempt on 2026-11-06 recorded it before refusing (fixed in forge#1876).
   - It is declared and has never been deployed.
3. Move the domain, then check again.
4. Merge reliant #2336 `feat/forge-demo-unhost`: the demo's own build is the supported path, and `DEMO_APP_HOST` is deprecated (it still mounts the demo shell in `prod/reliant-web`, until a future major).

## Commands

```bash
# 2. Build once, record the release, publish
cd frontends/web && pnpm install --frozen-lockfile && cd ../..
forge env build demo --target forge-demo --release demo-2026.11.06.1 --plan   # 1 step: npm run build → dist-demo, push
forge env build demo --target forge-demo --release demo-2026.11.06.1
forge env deploy demo demo-2026.11.06.1
forge obs check demo --wait --json        # 0 done · 1 regressed · 2 not proven good · 8 not enough data yet
forge env status demo                     # forge-demo: its *.sites.reliantlabs.dev URL
node frontends/web/scripts/demo/smoke-demo-site.mjs https://<that sites URL>

# 3. Move the domain (an org resource; this replaces prod/reliant-web's binding)
forge domain bind demo.reliantlabs.io --env demo --target forge-demo
forge domain show demo.reliantlabs.io --env demo   # serving: forge-demo in <demo env id>; all three records "ok"
```

`--target forge-demo` matters: without it the build also builds the `web` frontend found on disk, in the same directory (the `web` frontend of the same package; see deploy/kcl/demo/main.k).

**DNS: no change.**

- Verification and the edge certificate belong to the domain, not to the binding.
- These records stay as they are:
  - CNAME `demo` → `sites-edge.reliantlabs.dev`;
  - TXT `_reliant-challenge.demo`;
  - CNAME `_acme-challenge_c7n7ibvdftkcgcub.demo` → `9b8d63ea-….authorize.certificatemanager.goog`.

## Checks (after step 3)

```bash
curl -sI https://demo.reliantlabs.io/ | grep -i 'via: 1.1 google'          # served through the edge
curl -s  https://demo.reliantlabs.io/ | grep -o '<title>[^<]*'            # forge demo · Reliant
curl -s  https://demo.reliantlabs.io/config.js                            # only the DEMO_* / OTEL_* keys above
node frontends/web/scripts/demo/smoke-demo-site.mjs https://demo.reliantlabs.io
```

The smoke script loads `/` and 12 console tabs in headless Chrome. It exits 1 on any of these:

- a request to another origin, or a 4xx/5xx response;
- a page error, or a CORS console error;
- `/` not landing on `/forge/demo`;
- a page without `data-shell="demo"`.

It also counts "Sample data isn't available" per tab.

**The tour on landing.** `/` lands on `/forge/demo`. Since the UX change (feat/demo-ux), the tour no longer starts by itself: a toast offers it ("Take the guided tour"), and per-view tips show. The original ask was "the tour running"; this follows the newer "explore first" direction. Say if you want auto-start back.

## Rollback

Do it at once if any check after step 3 fails. Moving the domain back is instant: its verification and
certificate belong to the domain, so no DNS changes.

```bash
# 1. The domain back to prod's reliant-web (from a control-plane checkout)
cd ../control-plane
FORGE_ALLOW_VERSION_SKEW=1 reliant forge domain bind demo.reliantlabs.io --env prod --target reliant-web
FORGE_ALLOW_VERSION_SKEW=1 reliant forge domain show demo.reliantlabs.io --env prod   # serving: reliant-web

# 2. Only if the demo env itself must go back: its previous release (an ordinary deploy, BEHIND)
cd ../reliant
forge env status demo                       # lists the releases the env ran
forge env deploy demo <previous version>    # read the plan's "direction: BEHIND" before --yes
```

A static site has no migrations or data, so deploying the demo env's previous release is a
complete rollback for it. Step 1 restores exactly what served before the cutover: whatever
build prod's reliant-web runs, which still mounts the demo shell on that host (`DEMO_APP_HOST`,
deprecated but kept working after step 4 too).

## Follow-ups

- **control-plane** `deploy/kcl/prod/main.k`: `_demo_url` is in `_cors_origins` "for the phase-2 demo-data gateway and for the previous build's calls". After the cutover, the demo calls no API, so it can go (owner's call: a prod config change).
- **Phase 2**, a live demo-data gateway: set `DEMO_DATA_SOURCE=remote` and `DEMO_DATA_URL=<gateway>` in `deploy/kcl/demo/main.k`. A CORS entry would then be needed on the gateway, not on prod.
