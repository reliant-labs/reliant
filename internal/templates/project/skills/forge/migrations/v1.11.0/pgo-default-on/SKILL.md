---
name: migrations/v1.11.0/pgo-default-on
description: Profile-guided builds turn on by themselves wherever an env has CPU profiles (forge v1.11.0). A project whose hosted env builds a Go binary gets `.github/workflows/pgo-refresh.yml` on its next `forge generate` (new projects at `forge project new`). Every Monday it runs `forge pgo pull --env <prod>` and opens a pull request with each Go binary's cmd/<bin>/default.pgo when the env's profiles changed it, and does nothing where there are none. It needs a FORGE_OBS_TOKEN secret with deploy:read,obs:read. An existing project's profile collection is not changed. Hosted envs scaffolded since v1.10.0 declare profiles on. Use when a deploy/kcl/<env>/main.k binds forge.OnHosted and no cmd/<bin>/default.pgo is committed yet.
version: v1.11.0
detection: grep -lqs 'forge.OnHosted' deploy/kcl/*/main.k && ! grep -qs 'pgo-refresh: false' forge.yaml && ! ls cmd/*/default.pgo >/dev/null 2>&1 && forge-detect pgo-refresh-due
---

# Profile-guided builds turn on where profiles exist (v1.11.0)

Since v1.8.0, forge has built every Go binary with its committed
`cmd/<bin>/default.pgo` (Go's default `-pgo=auto`), and
`forge pgo pull --env prod` writes that file from the env's CPU profiles.
Nothing ran the pull for you. v1.11.0 adds a workflow that does, and it is
written into your project by itself.

## What changes by itself

**`.github/workflows/pgo-refresh.yml` appears on your next `forge generate`**
when a hosted env builds a Go binary (`ci.provider: github`): one whose
workloads run a `cmd/<bin>` main package of this checkout, the binaries
`forge pgo pull` writes profiles for. A hosted env that only serves a static
site gets none. It is a scaffold-once file like `release.yml`: once written it
is yours, forge never rewrites it, and a deleted one stays deleted (the birth
record under `.forge/scaffolded/` remembers it, so commit that). Every Monday
it pulls the last hosted env in promotion order that builds a Go binary. Then:

- If the env has profiles and a binary's profile changed, it opens (or
  updates) one pull request with the new `default.pgo`. Merging that PR turns
  PGO on for the binary, and every later build uses the profile.
- If the env has no profiles, or too little CPU in the week
  (`pgo.min_weight`), it commits nothing and ends green.

A build never fetches a profile. It reads the committed file, so a build can
always be reproduced from its commit, and a missing profile never fails a
build.

**Your envs still collect what they declared.** On `forge.OnHosted`, profiles
are off unless the env sets
`signals = forge.ObservabilitySignals {profiles = True}`. Every hosted env that
`forge project new` scaffolds since v1.10.0 sets it. An older project's envs
are left as they are. Profiles count toward the env's telemetry allowance,
like logs and traces.

## What to do

1. Store the token the workflow reads. It needs exactly these scopes:
   `deploy:read,obs:read`. The pull looks the env up, then reads its
   profiles, and it never deploys. Mint the token under Settings → API tokens
   in the web app, then run:

   ```bash
   gh secret set FORGE_OBS_TOKEN
   ```

   Until the secret is set, the job prints a notice and ends green.
2. If an env does not collect profiles yet, it has nothing to pull. To turn
   collection on, add `signals = forge.ObservabilitySignals {profiles = True}`
   to that env's `forge.Observability` (or add
   `observability = forge.Observability {signals = forge.ObservabilitySignals {profiles = True}}`
   to the bundle) and redeploy. That is your call: it adds a 5s CPU profile
   every 5 minutes per process, plus a live-heap snapshot every two hours, to
   the env's telemetry.
3. Optional, to start now instead of waiting for Monday:

   ```bash
   forge pgo pull --env prod   # writes cmd/<bin>/default.pgo per Go binary with data
   forge pgo status            # provenance, age and config per binary
   git add cmd/*/default.pgo && git commit -m "chore(pgo): first default.pgo from prod"
   ```

To decline the workflow, set `ci.workflows: {pgo-refresh: false}` in
forge.yaml before running `forge generate`, or delete the file afterwards.
A project that already refreshes its profiles is not given a second job: a
Reliant workflow named `.reliant/workflows/pgo-refresh.yaml`, or a workflow
under `.github/workflows/` or `.reliant/workflows/` that runs `forge pgo pull`,
counts, and `forge generate` names it in one line until forge.yaml says
`ci.workflows: {pgo-refresh: false}`.
More: `forge skill load observability/profiles`.
