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
| 2026-11-06 | `v1.0.17-…7ad8943c` → `v1.1.1-…b83896da` | #2327 | playbook `v1.1.0` recorded, nothing to edit (no `internal/app/login_broker.go`, no scaffolded sign-in screen, `frontends/web` is not hosted static); `.gitignore` un-ignores `.forge/migrations.json` so step 4's record can be committed; `gofmt` on `internal/toolexec/nats_bridge_cluster_test.go` (trailing blank line from #2321); `frontends/web` to `@reliantlabs/forge-web-runtime` 0.13.3. The upgrade wrote 8 template files reliant never had (`docker-compose.yml`, deleted on purpose in #1539, `idp-steps.yaml`, `.gitattributes`, four `deploy/observability/` ClickStack files, `frontends/web/eslint.config.mjs` beside our `eslint.config.js`): declined with `forge project disown --absent` (`.forge/scaffolded/`) | first bump through `forge project upgrade`. It re-adds scaffold-once files with no ledger entry, even ones deleted from git (forge bug `fd6cc6e7`); its needs-human list turns one goimports finding into six garbled items (forge bug `629d4cfa`) |
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
