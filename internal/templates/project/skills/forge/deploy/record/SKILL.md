---
name: deploy/record
description: Record deploys your OWN CI does — one `forge release record` step after the existing deploy job, in any repo and any language, no deploy/kcl, forge builds and applies nothing. Monitoring then gets deploy markers (`forge obs changes`), per-release verdicts (`forge obs check --wait`) and a Releases page labelled "recorded by CI". Copy-paste recipes for GitHub Actions (step and vendored composite action), GitLab CI and Buildkite. Use when a team keeps its pipeline and wants forge's release tracking or monitoring, or asks "can forge see my deploys?".
---

# Record deploys from your existing CI

Your pipeline keeps deploying exactly as it does today. After its deploy step
succeeds, one more step tells forge what went out:

```bash
forge release record --env prod --version "$VERSION" \
  --workload api --image "ghcr.io/acme/api@$DIGEST"
forge obs check prod --release "$VERSION" --wait
```

That is the whole integration. forge builds nothing and applies nothing.

**What you get.**

- A **deploy marker** per deploy: `forge obs changes prod` lists it ("promoted
  v1.4.0 (recorded by CI)"), and dashboards and issues line up against it.
- A **verdict per release**: `forge obs check prod --release v1.4.0 --wait`
  compares the window after the deploy with the one before (exit 0 good, 1
  regressed, 2 not proven, 8 not enough data yet). Recorded releases are judged
  like forge-built ones: from telemetry, by `service.version`.
- A **Releases page** in the console —
  `https://forge.reliantlabs.io/forge/p/<project>/releases/<version>` — showing
  the commit, the run and each workload's digest, labelled "recorded by CI".
  The command prints the link.

**How easy.** Easy: ~15 minutes the first time, then nothing per deploy.

| Step | Time |
|---|---|
| `forge.yaml` with `name: <project>` at the repo root (if there is none) | 1 min |
| A token with `deploy:write`, minted by a person in the web app (Settings → API tokens, https://forge.reliantlabs.io/settings/tokens; no deploy/kcl needed), stored as the CI secret `FORGE_CONTROL_PLANE_TOKEN` | 5 min |
| Paste the recipe below after the deploy job | 5 min |
| Run the pipeline once; `forge obs changes prod` shows the marker | 4 min |

## What a record is

- **A record-only release.** `--version` labels it; `--commit` (default: the
  CI commit — `GITHUB_SHA`, `CI_COMMIT_SHA`, `BUILDKITE_COMMIT` — or git HEAD)
  says what it was built from. `--workload <name> --image <ref>@sha256:<hex>`
  pairs, in order, record the digest each workload runs; with none it is
  **version-only**, which is valid only because it is record-only. Images must
  be pinned by digest: a tag names whatever was pushed last, not what was
  deployed.
- **A promotion into the env**, with actor `ci` inside CI, and the CI run (id
  and URL detected for GitHub Actions, GitLab CI and Buildkite; `--url` for
  anything else). It records no plan, because nothing forge planned was
  applied.
- **Idempotent.** A retried step records once (`release_created` and
  `promoted` are false in `--json`). The same version with other digests — or,
  version-only, at another commit — is refused as a conflict, never an
  overwrite: a version names one release.
- **Never posing as forge's own deploy.** The control plane refuses to bind a record-only
  release to an env forge itself deploys (a hosted env, or one bound to a
  cluster forge reconciles): reason `record_only_release`. The command refuses
  an env that has `deploy/kcl/<env>/main.k` the same way, before it cuts
  anything, on every ledger. When forge takes
  over deploying an env, its own `forge env deploy` releases follow the
  recorded ones in the same history.

## Where it records

| Situation | Ledger |
|---|---|
| `deploy/kcl/<env>/main.k` exists | none: refused (`record_only_release`), forge deploys that env |
| signed in (`FORGE_CONTROL_PLANE_TOKEN`, `forge login`, a credential helper; or `--token`) | Reliant cloud. The env is created there as **self-managed** on first use |
| `FORGE_LEDGER=machine`, or not signed in | this machine's ledger. It works, but monitoring cannot see it; a note says so. `forge ledger import --from-file-ledger` moves it later |

Monitoring needs sign-in: a record on this machine is history, not a marker.

## Recipes

Each one installs forge with the release installer (no Go toolchain), records,
then judges. Put them AFTER the step that deploys, so a failed deploy records
nothing.

### GitHub Actions — a step

```yaml
  record:
    needs: deploy                     # your existing deploy job
    runs-on: ubuntu-latest
    env:
      FORGE_CONTROL_PLANE_TOKEN: ${{ secrets.FORGE_CONTROL_PLANE_TOKEN }}
    steps:
      - uses: actions/checkout@v4     # for forge.yaml's name
      - name: Install forge
        run: |
          curl -fsSL https://raw.githubusercontent.com/reliant-labs/forge/main/install.sh | sh
          echo "$HOME/.local/bin" >> "$GITHUB_PATH"
      - name: Record the deploy
        run: |
          forge release record --env prod --version "${{ github.ref_name }}" \
            --workload api --image "ghcr.io/${{ github.repository }}/api@${{ needs.deploy.outputs.digest }}"
      - name: Judge it
        run: forge obs check prod --release "${{ github.ref_name }}" --wait
```

### GitHub Actions — a vendored composite action

forge's convention is a composite action VENDORED into your repository
(versioned with your repo, so a forge flag change cannot break a pipeline
pinned to a separately published action). Save as
`.github/actions/forge-record/action.yml`:

```yaml
name: forge release record + check
description: Record a deploy this pipeline did, then judge it with forge obs check.
inputs:
  env:       {description: Environment deployed to., required: true}
  version:   {description: Version label deployed., required: true}
  workloads: {description: "Space-separated name=ref@sha256:… pairs; empty for version-only.", required: false, default: ""}
  check:     {description: Run forge obs check --wait after recording., required: false, default: "true"}
  forge-version: {description: "Version of the forge CLI to install (default: latest).", required: false, default: ""}
runs:
  using: composite
  steps:
    - shell: bash
      env: {FORGE_VERSION: "${{ inputs.forge-version }}"}
      run: |
        curl -fsSL https://raw.githubusercontent.com/reliant-labs/forge/main/install.sh | sh
        echo "$HOME/.local/bin" >> "$GITHUB_PATH"
    - shell: bash
      env: {ENV: "${{ inputs.env }}", VERSION: "${{ inputs.version }}", WORKLOADS: "${{ inputs.workloads }}"}
      run: |
        set -euo pipefail
        args=()
        for pair in $WORKLOADS; do args+=(--workload "${pair%%=*}" --image "${pair#*=}"); done
        forge release record --env "$ENV" --version "$VERSION" "${args[@]}" --json > forge-record.json
        jq -r '"recorded \(.version) to \(.env): \(.console_url // .location)"' forge-record.json >> "$GITHUB_STEP_SUMMARY"
    - if: inputs.check == 'true'
      shell: bash
      run: forge obs check "${{ inputs.env }}" --release "${{ inputs.version }}" --wait
```

and use it:

```yaml
      - uses: ./.github/actions/forge-record
        env: {FORGE_CONTROL_PLANE_TOKEN: "${{ secrets.FORGE_CONTROL_PLANE_TOKEN }}"}
        with:
          env: prod
          version: ${{ github.ref_name }}
          workloads: api=ghcr.io/acme/api@${{ needs.deploy.outputs.digest }}
```

### GitLab CI

```yaml
record:
  stage: .post                        # after your deploy stage
  needs: [deploy]
  image: debian:stable-slim
  variables:
    FORGE_CONTROL_PLANE_TOKEN: $FORGE_CONTROL_PLANE_TOKEN   # masked CI/CD variable
  script:
    - apt-get update -qq && apt-get install -y -qq curl ca-certificates git >/dev/null
    - curl -fsSL https://raw.githubusercontent.com/reliant-labs/forge/main/install.sh | sh
    - export PATH="$HOME/.local/bin:$PATH"
    - forge release record --env prod --version "$CI_COMMIT_TAG" --workload api --image "$CI_REGISTRY_IMAGE/api@$API_DIGEST"
    - forge obs check prod --release "$CI_COMMIT_TAG" --wait
  rules:
    - if: $CI_COMMIT_TAG
```

The commit (`CI_COMMIT_SHA`) and the pipeline (`CI_PIPELINE_ID`,
`CI_PIPELINE_URL`) are detected.

### Buildkite

```yaml
steps:
  - label: ":rocket: deploy"
    key: deploy
    command: ./scripts/deploy.sh
  - label: ":memo: record"
    depends_on: deploy
    command: |
      curl -fsSL https://raw.githubusercontent.com/reliant-labs/forge/main/install.sh | sh
      export PATH="$$HOME/.local/bin:$$PATH"
      forge release record --env prod --version "$$BUILDKITE_TAG" \
        --workload api --image "$$(buildkite-agent meta-data get api-image)"
      forge obs check prod --release "$$BUILDKITE_TAG" --wait
```

Store the token as a pipeline secret named `FORGE_CONTROL_PLANE_TOKEN`. The
commit (`BUILDKITE_COMMIT`) and the build (`BUILDKITE_PIPELINE_SLUG`,
`BUILDKITE_BUILD_NUMBER`, `BUILDKITE_BUILD_URL`) are detected.

### Anything else

Run the two commands after the deploy. Pass `--commit` and `--url` when the CI
is not one of the three above; the `--url` then also identifies the run, so a
retry of the same run records once.

## Reading `obs check` in CI

Branch on its exit code, never on its text: **0** done; **1** the release
regressed — run the document's `suggested[]` commands and fix forward; **2** it
could not be proved good — report "not proven good", never "passed"; **8** not
enough data yet — check again later. Load `observability/query` for the
document.

Make the judgement meaningful: set `service.version` in your telemetry to the
same value you pass as `--version` (the tag or the git SHA). Without it, the
verdict cannot tell the new release's traffic from the old.

## JSON

`--json` prints one object: `project`, `env`, `version`, `commit`, `workloads`
(name → ref@digest), `ledger` (`hosted` | `machine`), `location`,
`release_created`, `promoted`, `promotion_id`, `run_url`, `console_url`.

## Next

- Monitoring first, if it is not set up yet: `observability`.
- Hand the deploy to forge when you are ready: `deploy` (`forge env deploy`); the
  recorded history stays in the same ledger, and forge's own releases follow it.
