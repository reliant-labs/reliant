# Start anywhere: forge for existing projects, one independent piece at a time

**Status:** approved by the owner on 2026-11-11 ("yes let's do it all"). Being
built in waves; the front door (below) is the first piece to ship.
**Date:** 2026-11-11
**Related:** [external-runtime.md](external-runtime.md) (the `forge.OnExternal`
binding this design adopts), [docs/benefits.md](../benefits.md) (the registry
the front door renders from).

## Summary

Every forge capability must be usable on its own, starting from an existing
app, its existing CI/CD and its existing infrastructure, in any language. There
is no required order and no rewrite. An agent that meets forge in some
repository should be told what forge does for **that** repository today, how to
start, and how long it will take — and should be able to stop after any step
with something that works.

Today forge presents itself the other way round: the MCP server introduces
"tools for forge projects", the skill catalog leads with forge's own
architecture, and `forge lint` fails lanes that only make sense in a forge
project. Much of what makes forge valuable already works without one; it is
just not offered.

## Principle

The suggested journey, with entry at any step:

1. **Monitor.** Send OpenTelemetry to Reliant: logs, traces, issues, alerts.
   This is the hook.
2. **Record deploys.** The user's CI keeps deploying; one call records each
   deploy. Monitoring gains deploy markers, per-release health (`obs check`) and
   `obs changes`.
3. **Prove parity.** Describe the deploy in a manifest plus a release hook;
   `forge env verify` shows no diff against what is live. It builds, deploys and
   records nothing.
4. **Let forge drive.** The same hook now deploys; forge builds once, promotes
   by digest and gates on `obs check`.
5. **Go further.** Secrets, migrations (through a hook), environments, codegen,
   hosting, forge CI.

Each later step only adds to the manifest; nothing written earlier is
rewritten.

## Decisions

These were settled with the owner on 2026-11-11 and are not reopened by the
work that implements them.

1. **Every piece works on its own**, in any repo and any language, with the
   adoption journey above and entry at any step.
2. **Monitoring is the hook, and it requires sign-in** (`forge login` or a
   control-plane token). There is no claimable or anonymous path for
   observability.
3. **Dashboards live at `https://forge.reliantlabs.io`.** The console routes are
   `/forge/p/<project>/observability`, `/observability/dashboards`, `/releases`,
   `/releases/<version>`, `/environments/<env>`, `/monitoring`,
   `/workloads/<env>/<name>`, `/activity` and `/settings`. forge prints these
   links where they help: after `obs init`, after `release record`, in
   `obs status`.
4. **`forge env verify <env>` is a new, side-effect-free command.** It builds,
   records and deploys nothing. `--plan-only` is not extended for it, because
   `--plan-only` builds, pushes and cuts a release first.
5. **Version-only releases** (no image artifact) are allowed in the ledger, for
   CI that only records.
6. **Telemetry ingest tokens are mintable by the signed-in user**, per
   environment and revocable, with a public OTLP endpoint. (Security and billing
   review apply.)
7. **`obs check` may use an inferred deploy marker**: the first time telemetry
   shows a new `service.version`. It is always labelled "inferred".
8. **forge.yaml gains switch sections**: `envs:`, `monitoring:`, `releases:`,
   `database.migrations:`. YAML holds switches and simple values; KCL holds
   topology. The same fact declared in both for one environment is a load error,
   and KCL is the long-term home. Both lower into the same internal models —
   `obsbackend.Facts`, the `pkg/release` records and ledger, `DatabaseConfig` —
   never a second system.
9. **`forge.External` returns as a runtime binding, `forge.OnExternal`**, beside
   `OnHosted` and `OnCluster` (see [external-runtime.md](external-runtime.md)).
   It has four optional argv hooks, run with no shell: `status` (prints JSON
   matching `ObservedItem`), `plan` (exit 0 = no change, 2 = changes), `deploy`
   and `health`. No hooks means record-only.
10. **The skill and the MCP server are marketing.** They sell forge's benefits
    to an LLM working in any repo. A skill can be long and detailed; what it
    makes short is the user's process, with estimates the agent can quote
    ("easy, ~1 hour: step A 10 min, …").

## One manifest

`forge.yaml` stays the single entry point. A section is on when it is present.
The smallest manifest for an entry point is `name:` plus that one section.

```yaml
name: shop
envs: [staging, prod] # enough for monitoring and recording; no deploy/kcl needed

monitoring: {} # hosted by default; `endpoint:` to point elsewhere

releases:
  workloads:
    api:
      hook: # forge.OnExternal, as a runtime binding
        status: [./scripts/live-version.sh] # prints {digest, version}
        plan: [helm, diff, upgrade, api, ./chart] # exit 0 = no change, 2 = changes
        deploy: [helm, upgrade, api, ./chart] # only once forge drives

database:
  migrations: { dir: migrations, tool: goose, apply: [goose, up] }
```

None of these sections exists yet; each lands with its piece.

## The front door: MCP, benefits, skills and lint, for any repo

The MCP server is where a new user's agent usually meets forge first, so it is
the first piece built. Four parts, each shipping only what works today.

**MCP instructions lead with the repo the agent is in.** What works with no
forge.yaml comes first: the lint lanes that run standalone, the playbooks, the
runtime libraries, the no-account static-site deploy, and monitoring plus deploy
history once forge deploys the app (sign-in required). The instructions route to
a new `benefits` tool and to `skill_load adopt`. The tools that need forge's
codegen (`shapes`, `annotations`, `audit`, `generate`) are described as what the
repo gains once it adopts codegen.

**`benefits.yaml` gains an adoption axis.** Each benefit may carry
`scope: any | forge-project` (default `forge-project`, so no existing entry
changes), and a `scope: any` entry must carry `adopt:` (the one-line entry
recipe) and `estimate:` (the agent's time to first value, `~<n> <unit>`). Every
`scope: any` claim is checked by running it in a scratch repo with no forge.yaml
before it lands. `forge project benefits` and the MCP `benefits` tool print the
entries that apply to a directory: the `scope: any` ones outside a forge project,
every shipped one inside. docs/benefits.md gains a "Works in any repo" table.
The command sits under `forge project`, beside `capabilities` and `libraries`,
rather than at the root, which the CLI consolidation keeps to fourteen nouns.

**The skill catalog splits by applicability.** A skill declares `applies: any`
in its frontmatter when it is useful in a repo with no forge.yaml. In such a
repo, `forge skill list` (and MCP `skill_list`) shows an "Applies to your repo"
group first. The list is not hard-coded: the frontmatter is the declaration.

**Lint in a non-forge repo skips project-only lanes with a reason.** A bare
`forge lint` failed `db-grants` ("could not apply db/migrations to a shadow
postgres") in any repo whose migrations do not apply from an empty database, and
failed golangci-lint and the contract linter in a repo with no Go module. With
no forge.yaml those lanes now skip, each naming why. Migration safety keeps
gating. No rule is added or tightened, so no lint rollout row is needed.

## The skill

One skill, `adopt` ("Bring your app — start anywhere"), split into sub-skills
because a shipped skill is capped at 24 KB. Its reader is an LLM, so it is long
and specific; what it makes short is the user's process. After reading it an
agent surveys the repo and tells the user, for example, "adopting forge's
migration checks here is easy — about 15 minutes: run the lint (5 min, I'll do
it), fix or annotate what it finds (10 min)".

It carries:

- a pitch whose every claim names a benefit id from the registry;
- a survey checklist (languages, services, migrations tool and layout, CI,
  deploy target, existing OpenTelemetry);
- the estimate rubric (per step, scaled by services, languages and existing
  instrumentation) and the phrasing template;
- the entry points as a menu, not a ladder: linters, playbooks, monitoring,
  runtime SDKs, record deploys, parity, forge-driven deploys, migrations,
  secrets, hosting, codegen, CI;
- forge's own reference code (`pkg/observe`, `pkg/serverkit`,
  `@reliantlabs/forge-web-runtime/node`'s `initNodeTelemetry`) with "translate to
  the project's language, keep the attribute names";
- "leaving is easy too": `forge env render`, any OTLP backend, ledger export.

It describes only what ships on main: an agent follows a skill literally.
Pieces still being built (standalone monitoring and `obs init`, `release
record`, `forge.OnExternal` and `env verify`, the migrations hook, deploy-only
adopt) appear as one-line "coming soon" pointers and get their step-by-step when
they land. The estimates are the acceptance test for "easy": when the honest
estimate for a step is long, the product work for it is not done.

## Work, by piece

### 1. Monitoring (first)

Blockers today: the obs commands render `deploy/kcl` to find their backend;
ingest tokens are minted only for control-plane's own pods; the public OTLP
endpoint is off in prod (`OTLP_PUBLIC_ENABLED = False` in control-plane's
`deploy/kcl/lib/otlp_public.k`).

- control-plane: an RPC to mint, list and revoke per-env telemetry tokens
  (`internal/telemetrytoken`), and the public OTLP endpoint switched on.
  **Blocker.**
- forge: the obs backend resolves from `forge.yaml monitoring:` or flags when
  there is no KCL env.
- `forge obs init`: creates the hosted env as `self_managed` (control-plane's
  `EnsureEnvironment` already supports it) with no deploy, and prints the
  dashboard link.
- control-plane: release health keyed on the first-seen `service.version` when
  no promotion exists, labelled "inferred" (decision 7).
- Alerts outside KCL, and a `forge obs receivers` CLI over the existing
  AlertingService RPCs.
- `forge obs up`: the local ClickStack alone, for any app.
- Health and log-format checks become optional for workloads forge did not
  build.

### 2. Record deploys

- `forge release record --env --workload --version [--image @sha256]
[--commit] [--url]` → the existing `CutRelease` plus `Promote(actor=ci)`, or
  the local file ledger.
- control-plane: an artifact-less (version-only) release (decision 5), and
  `Promote` without a plan. Today `CutRelease` requires at least one artifact.
- Env discovery accepts YAML `envs:` (today it requires `deploy/kcl`).
- A GitHub Action wrapping `release record` and `obs check --wait`.

### 3–4. Release hook and parity

`forge.External` (`deploy_cmd` / `health_cmd` through `sh -c`) was removed in
#284 with no replacement. It returns as `forge.OnExternal` (decision 9).

- `forge env verify <env>`: runs `status` and `plan`; builds and records
  nothing (decision 4). Reports image parity and config parity separately; a
  missing hook is "unverified", never "no diff". Reuses the `env diff` status
  vocabulary and `--live-diff` for clusters.
- Deploys through the hook write the ledger and `ReportDeployAttempt`.
- Remove the stale External references the removal left behind.

### 5. The rest

- Migrations hook: `database.migrations {dir, tool, apply, status}`. forge
  applies into the shadow database through the user's tool and keeps ORM
  projection, seeding and the deploy-time run. Lint rules declare which tools
  they apply to; others report "skipped (tool: goose)". Fix
  `database.migrations_dir`, which is advertised but `yaml:"-"` (forge bug
  5d3c6d68).
- Deploy-only adopt: `forge project new --in-place` writes only forge.yaml (and
  `deploy/kcl` if asked): no Go scaffold, Taskfile, CI or memory file unless
  requested.
- Folder-level ownership (`scaffold.exclude` globs), `harness: none`, a per-
  workflow CI opt-out, a per-lane lint off switch, and an orphan report when a
  feature is turned off.
- Environments: a shared base with per-env deltas; preview envs with a TTL;
  enforced promotion gates.
- Polyrepo: share KCL through GitSource (not go.mod); `DockerBuild.context`
  accepts a GitSource.
- A memory file rendered from what the project actually uses (the sections
  present, unmanaged services, the repo's own test command), not the greenfield
  template.

### Existing skills

- Split the `migration*` skills into "port into forge's layout" and "adopt forge
  around your code".
- Reconcile `migration-service` with `deploy/byo-yaml`.
- Restore an external-deploy recipe section (Fly, Cloud Run, ECS, Helm, Argo)
  under `forge.OnExternal` once it ships.

## First milestone: "monitoring in 2 minutes"

The control-plane token RPC and public endpoint → the obs backend from
forge.yaml → `forge obs init` → inferred release health → `forge release
record` → the `adopt` skill's monitoring and deploy-recording sections. The rest
follows the journey order.
