# demo.reliantlabs.io: the cutover runbook

> **Since 2026-11-10 the demo ships with prod.** Owner decision: "put the demo into the prod
> env, so it releases with prod". control-plane's prod env declares `forge-demo`
> (`control-plane/deploy/kcl/lib/reliant_web.k`). It is the same demo build as below, from
> the same pinned reliant commit as reliant-web, so every rc-train promotion ships it by
> digest. Read [Into prod](#into-prod-2026-11-10) first. The `demo` env and the rest of this
> page are the previous path: deprecated, and kept working until the retirement follow-up.


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

## Into prod (2026-11-10)

The alternatives considered, and why each was rejected (a second promote step for the `demo` env, a demo declaration imported from reliant's KCL, serving it from reliant-web by hostname), are in the control-plane PR that made this change.


### What changes

| | `demo` env (deprecated) | prod |
|---|---|---|
| Declared in | reliant `deploy/kcl/demo/main.k` | control-plane `deploy/kcl/lib/reliant_web.k` `forge_demo`, listed in `deploy/kcl/prod/main.k` `frontends` |
| Built from | the reliant checkout you deploy from (`path`) | reliant-web's `GitSource`: one literal, so `task pin:reliant` and the train's pins move both builds together |
| Shipped by | `forge env build/deploy demo`, by hand | the rc-train: `forge env build prod` at the cut, `forge env deploy prod` at the promote |
| Activation | when deployed | `release_dependencies = {}`. The demo calls no API, so it does not wait for the server rollout (reliant-web does) |
| Runtime config | `DEMO_*`, `OTEL_*` | the same, plus `AUTH_MODE = "self-hosted"`. forge requires a cross-repo document to state its mode selector, and `self-hosted` is the same app as unset. The platform's `ENVIRONMENT` reads `prod`. |

Neither the demo bundle nor the build target changes. A demo fix lands on reliant `main`, and
the next release ships it. Label the PR `release:asap` to ship sooner.

### The domain bind contract

What the move and the rollback rely on (forge's `forge domain`, `internal/cli/domain_cmd.go`):

- A domain is an org resource with **at most one binding**: one environment and one target in
  it. The move is not two envs sharing the domain.
- `forge domain bind` **replaces** whatever binding the domain has, in one call
  (`CreateDomainBinding`). There is no unbind step first, so there is no moment where the domain
  serves nothing. This is the same operation step 3 above used to move the domain from
  control-plane `prod/reliant-web` to reliant `demo`.
- A binding is not a deployment. Rebinding the domain away from the `demo` env leaves the env's
  `forge-demo` site deployed and serving on its `*.sites.reliantlabs.dev` URL. That deployed site
  is the rollback target, and the old binding is not kept anywhere. Rolling back is one more
  `bind`.
- `--env` is resolved inside **the project of the checkout you run from**. `--env prod` must run
  from a control-plane checkout, and `--env demo` from a reliant checkout.
- The binding carries its HSTS policy, and a rebind without `--hsts-include-subdomains` turns
  that policy off. demo.reliantlabs.io was bound without the flag (step 3 above), so neither bind
  below passes it. Check the `hsts:` line of `domain show` before each bind and keep it the same.

### The one-time domain move

Do this after the first prod promotion whose release carries `forge-demo`. Until then the
domain stays on the `demo` env, so there is no gap.

```bash
# reliant checkout: confirm the rollback target is deployed and serving before moving away from it
forge domain show demo.reliantlabs.io --env demo     # serving: forge-demo in <demo env id>; note hsts
forge env status demo                                # forge-demo: its *.sites.reliantlabs.dev URL
node frontends/web/scripts/demo/smoke-demo-site.mjs https://<demo's sites URL>

# control-plane checkout at that release: check prod's copy, then move the binding (replaces demo's)
reliant forge env status prod                        # forge-demo: its *.sites.reliantlabs.dev URL
node ../reliant/frontends/web/scripts/demo/smoke-demo-site.mjs https://<prod's sites URL>
reliant forge domain bind demo.reliantlabs.io --env prod --target forge-demo
reliant forge domain show demo.reliantlabs.io --env prod   # serving: forge-demo in prod; records "ok"
```

DNS does not change. The verification and the edge certificate belong to the domain, as
in step 3 above, so they survive every rebind.

### Proof that the demo is served from the prod release

```bash
# control-plane checkout at the promoted release: /version.json's commit = the pinned
# reliant ref, served through the edge (via: 1.1 google), config.js uncached. Exit 0.
scripts/check-web-version.sh --url https://demo.reliantlabs.io
node ../reliant/frontends/web/scripts/demo/smoke-demo-site.mjs https://demo.reliantlabs.io
```

`forge obs check prod` does not judge the demo: it judges server spans. Once the domain has
moved, `scripts/verify-prod-release.sh` should run the `check-web-version.sh` line above on
every promotion (follow-up). Adding it before the move would fail every promotion, because
the domain still serves the `demo` env's older build.

### Rollback

- **Domain**: back to the `demo` env. Its `forge-demo` site stays deployed after the move (a
  binding is not a deployment; see the contract above), so the rollback is one rebind, which
  replaces prod's binding. Run it from a reliant checkout, because `--env demo` resolves in
  reliant's project:

  ```bash
  node frontends/web/scripts/demo/smoke-demo-site.mjs https://<demo's sites URL>   # still serving
  forge domain bind demo.reliantlabs.io --env demo --target forge-demo
  forge domain show demo.reliantlabs.io --env demo   # serving: forge-demo in <demo env id>; records "ok"
  ```

  It takes effect at once, and DNS does not change.
- **A prod rollback below the first release that carries `forge-demo`** drops prod's demo
  site. Rebind the domain to the `demo` env first. Do not raise prod's rollback floor for
  it: floors gate migration compatibility, not a static site.

### Retiring the `demo` env (follow-up)

Do this once prod has served the domain through at least one more release:

- delete `deploy/kcl/demo/` and `.github/scripts/forge-demo-env.test.mjs`;
- delete the env on the control plane;
- drop this page's previous-path sections.
