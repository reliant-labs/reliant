# Upgrading forge in reliant

**reliant always moves to the newest forge.** Owner decision, 2026-11-05. This
file is the procedure for doing that, plus a log of every bump. The per-version
steps (what changed in a release, and what a project must do about it) are
forge's, and forge prints them; this file never copies them.

## Why the newest, always

reliant is forge's first and largest consumer, and it dogfoods two things at
once:

- **the newest forge**, so a forge regression shows up here before any other
  project pins it;
- **forge's upgrade process itself** (`forge project upgrade`, its codemods and
  its migration playbooks), so the path every other forge project takes is one
  we walked first.

So when a bump needs a small change in reliant (a lint finding the new forge
reports, a migration a new forge contract asks for, a test pinning a middleware
chain), that change is **part of the bump**, in the same PR. It is not separate
work for later. When the change is needed because forge did something wrong
(bad codegen, a lint false positive, a playbook that missed a step), the fix
belongs in forge (see "Friction" below).

forge's compatibility policy is what makes this cheap: within major 1 nothing
public breaks, and a major's breaks ship playbooks and codemods that `forge
project upgrade` surfaces and applies (`forge skill load migration-upgrade`,
"Compatibility policy").

## Where the steps for a release live: forge

Use the **target** forge for all of these, never the forge on `PATH` or the one
embedded in `reliant forge`. Those are whatever version was installed, and they
say so (`this forge is …, but forge.yaml pins …`).

```sh
forge skill load migration-upgrade        # how forge upgrades work: phases, exit codes, window
forge project upgrade list                # the playbooks THIS tree still needs, oldest first
forge skill load migrations/<version>     # one playbook, by the path `list` prints
```

## The procedure

How reliant pins forge (commit pseudo-versions of forge `main`, both forge modules
at one commit, `GOWORK=off` builds) is in `dev-docs/pinning.md`. The steps below
assume it.

1. **Pick the target: by default, forge's newest `main`.** Read it from the
   remote, not from a local `../forge`, which is often on another agent's branch,
   and resolve it to a commit **once**: `main` moves, so every later step uses
   this `$sha`/`$v`, never `main` again.

   ```sh
   export GOPRIVATE='github.com/reliant-labs/*'
   sha=$(git ls-remote https://github.com/reliant-labs/forge refs/heads/main | cut -f1)
   v=$(GOWORK=off go list -m -f '{{.Version}}' "github.com/reliant-labs/forge@$sha")
   F="env GOFLAGS=-trimpath FORGE_NO_HOOKS=1 go run github.com/reliant-labs/forge/cmd/forge@$v"
   ```

   The one exception is a reliant change that needs a forge fix not yet on
   `main`: set `sha` to that forge PR's head instead and follow
   `dev-docs/pinning.md` ("To land a change that needs an unmerged forge
   commit"): merge forge, then re-pin to forge's `main` before this PR merges
   (`task pin:ancestry` stays red until you do). "Newest" rules out picking an
   older forge, not pinning a fix ahead of its merge.

2. **See what the range needs** before writing anything:

   ```sh
   $F project upgrade --check --to "$v"     # migrations still needed, oldest first; planned phases
   ```

3. **Run the upgrade.** It does every mechanical step for the range: modules,
   codemods, templates, the `forge_version` pin, playbook automation, `forge
   generate`, lockfiles, then `forge lint`. What it cannot do is listed as "Still
   needs a person".

   ```sh
   $F project upgrade --to "$v"             # add --json for the machine-readable report
   ```

   Exit 0 means converged. Exit 9 means everything mechanical is applied and the
   `needs_human` list is the rest of your job. Exit 1 is a real failure, so read it.
   Exit 2 is retryable (a registry or lint verdict could not be reached).

4. **Apply the listed playbooks, in the order printed.** Follow each one's manual
   part, then record it with `$F project upgrade apply <id>`, which writes
   `.forge/migrations.json`. Commit that file.

5. **Run `task pin:forge -- "$sha"`.** After the upgrade this is mostly a
   no-op, and that is the point. It proves the pin survived `go mod tidy`,
   builds with `GOWORK=off` (what CI sees), re-runs the pinned `forge generate`,
   and regenerates `task generate:docs`. `generated/docs-source/reference/cli.md`
   embeds forge's whole command tree, so nearly every bump changes it.

6. **Fix what lint and build surface.** The upgrade's lint phase and step 5's
   build list them. A finding in reliant's code is fixed here, in this PR. A
   finding that is forge's fault is fixed in forge (see "Friction" below).

7. **Run scoped tests**, only what the bump reaches:

   ```sh
   task test:affected                       # go.mod changed, so this is the whole Go lane, -short
   task test:short -- ./internal/<pkg>/...  # each package you edited by hand
   ```

8. **Open the pin PR.** Title it `chore(pin): forge@<short sha> (<what it brings>)`.
   In the body, give from → to, the playbooks applied and what needed a person.
   Add this bump's row to the log below in the same PR. Open it non-draft, add
   no queue label, and stop: the pipeline reviews and gates it.

9. **Bump control-plane in lockstep when it needs it.** control-plane's forge must
   equal the forge of the reliant commit it pins: `check-forge-coherence.sh` fails
   a skew either way, because control-plane renders reliant's KCL. After the
   reliant PR lands, one control-plane PR moves both pins:

   ```sh
   task pin:forge REF=<forge sha> RELIANT=<the reliant commit that landed it>
   ```

   Open it when control-plane needs the new forge or the new reliant now.
   Otherwise the rc-train's pin PRs carry it (control-plane `docs/pinning.md`,
   "Coherence" and "Who moves the pins now").

## Friction

A bump is also a test of forge's upgrade path. When forge gets something wrong
(a codemod that misses a case, a playbook with a stale step, a lint false
positive, a regenerated file that churns for no reason), fix it in forge, or file
it with `reliant forge report-bug`. Then put the PR or bug link in the log row. A
reliant-side workaround with no forge link is a defect that ships to every other
project.

## Upgrade log

Newest first. "reliant-side migrations" is every hand change the bump needed
beyond `go.mod`/`go.sum`/`forge.yaml` and regenerated output. Rows before
2026-11-05 were reconstructed from git history, which did not record friction.

| Date | From → to (forge) | PR | reliant-side migrations | forge friction |
|---|---|---|---|---|
| 2026-11-12 | `v1.4.1-…6e4246f3` → `v1.4.1-…cdac1da2` | this PR (the owner-authorized analytics release; control-plane takes the same forge in its lockstep pin PR) | `frontends/web` and `electron` to `@reliantlabs/forge-web-runtime` ^0.13.11 (forge #2154's scaffold floor: the first release with `identifyUser` and the analytics tiers). The demo build runs `analytics: "full"` and calls `grantAnalyticsConsent()` before init, so its visitors keep a persistent visitor id and replay (`src/lib/telemetry.ts`, two tests). `forge project upgrade` converged with no codemod or playbook; `task pin:forge` regenerated the CLI reference and `project_gen.json` (forge #2075's built-in Overview, Users, Product and Acquisition dashboards and their sections). forge #2075 retired the generated `usage` dashboard, so `internal/dashspeccontract` now holds the built-in analytics dashboards to the same contract: every statement and trend reads only the unqualified tenant tables (`product_events`, `error_events`, CTEs aside), each read narrowed by the environment predicate, bound by forge's sql-panel compiler, refused against the server's database, and run on ClickHouse with a second environment kept apart (the hand-worked figures are forge's, in `pkg/obsbuiltin`). Brings forge #2075, #2154, #2155, #2156, #2153 and #2087. Post-deploy check and rollback: `forge-analytics-pin-rollout.md` | none in this bump. web-runtime 0.13.11 exports one replay chunk recorded at consent withdrawal after `disableTelemetry()` (0.13.10 did not), found by the landing page's consent check (report `be09619f`) |
| 2026-11-12 | `v1.3.1-…9e678198` → `v1.4.1-…6e4246f3` | this PR | `internal/grpc/server_test.go`: the user interceptor chain grew from 16 to 17 (forge #2068's resumed sign-in heartbeat after auth). `forge project upgrade` converged with nothing for a person. Brings forge #2152 (test factories never redeclare a hand-written name), which control-plane needs to pin forge #2127 (control-plane #2555). | forge #2152: the factories emitter redeclared control-plane's hand-written `NewDeployment` and dropped `FactoryOption` for a failing owner-aware factory; fixed in forge |
| 2026-11-11 | `v1.3.1-…e01ef3a1` → `v1.3.1-…9e678198` | this PR | Two migrations widen `access_tokens_scopes` to forge #2059's `sourcemaps:write` (the upgrade's one needs-human item; the NOT VALID + VALIDATE pair, as in #2205). `task pin:forge` regenerated the CLI reference, forge proto and config schema. Brings forge #2127: direct apply honours `delivery_units` (control-plane #2555). | none |
| 2026-11-11 | `v1.3.1-…de8e6d23` → `v1.3.1-…e01ef3a1` | this PR (unblocks #2534, which documents forge #2034's `adopt` skill) | none: `forge project upgrade --to` applied no codemod or playbook; `task pin:forge` regenerated the CLI reference, the generated observability spec (dashboard sections from forge #2010) and the web runtime-config schema (`ANALYTICS_MODE`). Brings forge #2034 (adopt skill, obs check reason codes), #2010, #1982 (rollback floors CLI), #1990 (debug sessions), #2050 (obs check judges a promotion from its live_at) among others. `internal/dashspeccontract` now holds every `trendSql` sparkline forge #2010 added to the same product_events scope contract as each panel's `sql` | none |
| 2026-11-09 | `v1.2.1-…9caded4a` → `v1.3.1-…de8e6d23` | this PR (CP #2323 coherent prerequisite) | Recorded newest Forge main once, includes merged #2013 underscore root command compatibility. `project upgrade` applied mechanical changes; followed and recorded `v1.4.0` replay playbook for demo: equivalent per-frontend `BrowserTelemetry {replay = True}` replaces env-wide signal/runtime override; no replay behavior change. Canonical pin script regenerates CLI docs and source. Added optional `PIN_BUILD_PACKAGES` (full default unchanged) for owner-required scoped verification. | Scoped CP lint stale descriptor issue fixed separately in forge #2017: embedded NewRootCmd omitted on-demand extractor registration. Not part of this pin until merged. |
| 2026-11-09 | `v1.2.1-…56e8024f` → `v1.2.1-…9caded4a` | this PR (the pin half of #2468, split by the merge shepherd; pinned to the first forge main whose push lane is green after #2001) | none: `forge project upgrade --to` converged with no codemod or playbook and changed no file beyond what `task pin:forge` regenerated (CLI reference: forge #1998 groups commands under 14 nouns and old spellings keep working; `project_gen.json` gains the generated product-usage dashboard; `otel_gen.ts` withholds session replay under GPC/DNT; `graph_gen.go` gains `ConstructionRollbackTimeout`, forge #1997, default zero = legacy Build). Installed components (`forge-ui/modal.tsx`, `sidebar_layout.tsx`) are not refreshed by a pin; their adoption stays in #2468. Brings forge #1971, #1976, #1980, #1984, #1986, #1988, #1991, #1993, #1997, #1998, #2000, #2001 among 35 commits | none |
| 2026-11-08 | `v1.2.1-…56e8024f` → `v1.2.1-…0a3a89d5` | #2468 (adoption only: its pin is superseded by #2491's `9caded4a`, which contains `0a3a89d5`) | Newest actual Forge main resolved once after #1986 merged: includes drawer #1971, verified-TLS #1976, legacy-dialog compatibility #1980/#1986, and additive nav `defaultExpanded`/`id` #1984. Install `modal` canonically (the canonical `sidebar_layout` adoption is deferred: main's #2496 sidebar adds controlled `open`/`onOpenChange` (forge #2011) and a drawer Tab trap that forge's `useModalDialog` lacks; it is re-adopted at the forge pin that fixes `useModalDialog`); preserve app-owned TanStack `link.tsx`. No crossed codemod/playbook. Consumer parity models real Tailwind stacking and adds whole-subtree legacy-preview scroll restoration regression; no app-owned overlay workaround. | Consumer adoption exposed external-overlay isolation and stale saved-overflow cleanup, fixed upstream in #1980/#1986. Initial lint misclassified ignored Vite build artifact `dist-demo/config.js` as required source (report `521df583`); final unscoped lint runs with this worktree's own build output relocated outside the checkout. |
| 2026-11-08 | `v1.2.1-…6d8c0ab6` → `v1.2.1-…56e8024f` | this PR | none: `forge project upgrade` converged with no codemod or playbook and changed no file beyond the pin; `task pin:forge` regenerated nothing (CLI reference unchanged). Brings forge #1960 (the trace page is selected before its per-trace columns are computed, which fixes slow `ListTraces`; control-plane takes it in the lockstep pin PR), #1958 (the dev `/_otel` proxy forwards only same-origin callers with an allowlisted header set; web-runtime source only, `frontends/web` keeps its published runtime), #1956 and #1957 (forge CI only) | none |
| 2026-11-08 | `v1.2.1-…88cb28c2` → `v1.2.1-…6d8c0ab6` | this PR | none: `forge project upgrade` applied no codemod or playbook and changed no file beyond the pin; `task pin:forge` regenerated only the CLI reference (`--rebuild` on `forge build`, `env build`, `env up`). Brings forge #1935 (`ShellBuild.inputs`: a ShellBuild whose declared inputs, command, env and pushed image are unchanged since its last successful build, and whose image the registry still serves, is skipped; control-plane's dev ShellBuilds adopt it in the lockstep pin PR), its follow-ups #1951, #1952 and #1955, #1953 (the dev `/_otel` token proxy forwards only bounded OTLP ingest POSTs) and #1954 (pgtest re-attaches to a dead shared server instead of failing) | none in forge. On this macOS host, the upgrade's lint phase listed 8 darwin-only findings that predate the bump (SA4023 in `internal/toolexec/daemonruntime/worktree_recovery_restore.go`, `unused` in `internal/procguard`, `internal/proctree` and `internal/worktreereclaim` for code only Linux builds call), and its frontend install failed building `better-sqlite3` natively under node 26. `task ci:merge`'s `-short` test phase failed only in host-bound packages (`internal/worktreereclaim` and `internal/provetest` cannot copy git's `fsmonitor--daemon.ipc` socket, and `internal/osutil` and `internal/llm/tools/shell` have process-group kill tests); all four fail the same way at the previous pin (`go test -short -modfile=<main's go.mod>`). Its lint, build, vet, generate and docs phases passed |
| 2026-11-08 | `v1.2.0` → `v1.2.1-…88cb28c2` | this PR (split out of #2411 with nothing from #2412) | `forge project upgrade` converged with no playbook (through the v1.2.0 release). Its lint phase needed one person at 49ab17277: forge's `accesstoken.AllScopes` gained `deploy:config:read` and `storage:read`, so the `forge:closed-set` CHECK `access_tokens_scopes` widens in two migrations, written with `forge db migration new --closed-set` then split NOT VALID + VALIDATE as in #2261 (`20261107234731_access_tokens_config_storage_scopes`, `20261107234747_…_validate`), and `TestExchangeToken_AllUserOrgCapabilities` expects both in the exchangeable set (plus a test that neither is delegated unheld). The upgrade raised `frontends/web` to `@reliantlabs/forge-web-runtime` 0.13.6 (published delta audited from the npm tarballs: only `mock-values.js`, RE2 patterns translated to JavaScript RegExp for generated mock values). CLI reference regenerated. Brings forge #1926 (web-runtime `interaction()`), #1930 (obs instance scope), #1931 (hosted dev telemetry) #1918 (pgtest owner leases: a standalone server holds `forge-owner.lock` and a stale sweep honours it, the root fix for the shared embedded Postgres losing its data files mid-run on the cicd gates, bug df26c327), and #1950 (container-free dev stacks with `dev_telemetry` and an optional control plane), which reliant's dev env adopts in a follow-up PR | adopting `dev_telemetry = "auto"` in the container-free dev env at 49ab17277 hit four forge defects, all fixed in forge #1950: render demanded `observability.local_port` with no possible local collector (`4bc4f56c`); `control_plane` on a `lifecycle = "local"` env made a signed-out `forge env up dev` fail in its deploy phase (`def7265e`); `forge obs status` shelled out to docker on an env with no ClickStack and hung where docker had no engine (`4dfe2b53`); `forge generate` reported a failing env check as a missing frontend (`7edd2e69`). `--closed-set` writes a plain CHECK that scans under ACCESS EXCLUSIVE, not the NOT VALID + VALIDATE pair this repo uses |
| 2026-11-08 | `v1.1.1-…7a1f5720` → `v1.2.0` (same commit) | (this PR) | none: the release train tagged forge v1.2.0 on 7a1f5720c (rc set v1.9.0-rc.21), and Go never writes a pseudo-version for a tagged commit, so control-plane's cut pinned `v1.2.0` while reliant pinned the pseudo-version of that commit; `check-forge-coherence.sh` compares the strings and failed every new cut ("AHEAD"). Pinning the tag makes both name one version | coherence and the cut's pin step should treat a tag and a pseudo-version of the same commit as equal (control-plane #2213) |
| 2026-11-07 | `v1.1.1-…48022520` → `v1.1.1-…7a1f5720` | this PR | none; no pending playbook, generated code and CLI docs unchanged. Includes merged forge #1916 local inert-data collection correction, #1917 fail-closed generation, and #1915 runtime timezone data | Initial upgrade failed safely on shared embedded Postgres 16 port 40017 already occupied; stale sweep was skipped, no emitter output deleted. Recovered with supported `FORGE_TEST_POSTGRES_URL` pointing to isolated Postgres 16, then `task pin:forge` build/generate/docs succeeded. No hand-edited generated output or version downgrade |
| 2026-11-07 | `v1.1.1-…57e4e1a4` → `v1.1.1-…48022520` | (this PR) | none: `forge project upgrade` converged with no playbooks; CLI reference regenerated (`forge env delete --id`, `env list --orphaned`) | none |
| 2026-11-07 | `v1.1.1-…4c2ecdca` → `v1.1.1-…57e4e1a4` | (this PR) | none: `forge project upgrade` converged with no playbooks; regenerated output unchanged | none |
| 2026-11-06 | `v1.1.1-…b83896da` → `v1.1.1-…4c2ecdca` | pending | adopt merged startup dependencies/chart prerequisites; regenerate hooks/service mock defaults; record `v1.2.0` after checking strict unit/demo transports stay unchanged; web runtime raised to `^0.13.5` for generated mutation helpers | generator retained runtime floor `0.13.3` although new hooks require helpers it lacks (reported `1d890791`); upgrade lint exposed a Linux-only shared test helper (moved unchanged to a portable test file) and existing Darwin fail-closed recovery staticcheck findings; vendored secret-value service excluded from browser TS inputs; hook phase ordering follow-up required for one-pass mock cleanup; no generated files hand-edited |
| 2026-11-06 | `v1.0.17-…7ad8943c` → `v1.1.1-…b83896da` | #2327 | playbook `v1.1.0`: section 3 (open redirect) applied to the app-owned guard: `frontends/web/src/lib/returnTo.ts` `isSafeReturnTo` now parses like the browser (refuses `/\evil.com`, `/%09/evil.com`, `/a/..//evil.com`), and seven inline copies of the old prefix check now call it; sections 1–2 and 4 do not apply (no `internal/app/login_broker.go`; `frontends/web` is not hosted static and the server sends `X-Frame-Options: DENY`); `.gitignore` un-ignores `.forge/migrations.json` so step 4's record can be committed; `gofmt` on `internal/toolexec/nats_bridge_cluster_test.go` (trailing blank line from #2321); `frontends/web` to `@reliantlabs/forge-web-runtime` 0.13.3 (published delta audited from the npm tarballs: only `telemetry.js`, from forge #1828, which drops FCP/LCP painted after the page was first hidden, and `mock-transport`, from forge #1798, which adds opt-in options only; evidence in the #2327 thread). The upgrade wrote 8 template files reliant never had (`docker-compose.yml`, deleted on purpose in #1539, `idp-steps.yaml`, `.gitattributes`, four `deploy/observability/` ClickStack files, `frontends/web/eslint.config.mjs` beside our `eslint.config.js`): declined with `forge project disown --absent` (`.forge/scaffolded/`) | first bump through `forge project upgrade`. It re-adds scaffold-once files with no ledger entry, even ones deleted from git (forge bug `fd6cc6e7`); its needs-human list turns one goimports finding into six garbled items (forge bug `629d4cfa`) |
| 2026-11-05 | `v1.0.16-…8b49eee3` → `v1.0.16-…10926048` | #2242 | none (CLI reference regenerated) | not recorded |
| 2026-11-05 | `v1.0.16-…896c9108` → `v1.0.16-…8b49eee3` | #2205 | two migrations widening `access_tokens` scopes for forge #1757's runs/triggers scopes; regenerated alert defaults | not recorded |
| 2026-11-05 | `v1.0.15-…5f5293e7` → `v1.0.16-…896c9108` | #2193 | two staticcheck findings the new forge's lint reported (S1021 `internal/workflow/validation/secrets.go`, S1025 `internal/stepsecrets/stepsecrets_test.go`) | not recorded |
| 2026-11-04 | `v1.0.15-…fe9fb2df` → `v1.0.15-…5f5293e7` | #2164 | `frontends/web` to `@reliantlabs/forge-web-runtime` 0.13.1 (package.json, lockfile) | not recorded |
| 2026-11-04 | `v1.0.15-…0dd8256d` → `v1.0.15-…fe9fb2df` | #2153 | `internal/grpc/server_test.go`: the interceptor chain grew from 14 to 16 (request span carrying the browser session, identity stamp) | not recorded |
| 2026-11-03 | `v1.0.15-…02954132` → `v1.0.15-…0dd8256d` | #2122 | none | not recorded |
| 2026-11-03 | `v1.0.14-…a88d3c30` → `v1.0.15-…02954132` | #2120 | none (CLI reference regenerated) | not recorded |
| 2026-11-03 | `v1.0.14-…468c64dc` → `v1.0.14-…a88d3c30` | #2116 | `.license-allowlist`: `hashicorp/go-uuid` (MPL-2.0, transitive of prometheus/common) | not recorded |
| 2026-11-02 | `v1.0.13-…d09e8cb4` → `v1.0.14-…468c64dc` | #2092 | none by hand; regenerated `db/grants/` and the `.forge/scaffolded/` birth ledger | not recorded |
| 2026-11-02 | `v1.0.12` → `v1.0.13-…d09e8cb4` | #2091 | none (CLI reference regenerated) | not recorded |
