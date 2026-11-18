---
name: deploy/preview
description: Per-PR and per-branch preview environments — `forge env preview create|delete|name` and `forge env gc`. The committed template env they copy, how an instance is named, its TTL and who deletes it when it expires (the control plane for a hosted preview, `forge env gc` for a cluster you operate), the namespace-per-preview convention, and the GitHub Actions recipe that creates, deploys, comments the URL and deletes on close.
---

# Preview environments

A preview is a copy of a **committed template env**, named for a pull request
or a branch, that **expires**. Once created it is an ordinary env: `forge env
deploy`, `status`, `diff` and `obs check` all work on it unchanged.

```bash
forge env preview create --pr 123 --ttl 72h   # writes deploy/kcl/preview-pr-123/
forge env deploy preview-pr-123 --yes         # build once, record, promote: its own ledger
forge env preview delete --pr 123 --yes       # on PR close
forge env gc --yes                            # delete whatever expired
```

## The template env

Commit one env the previews copy, by default `deploy/kcl/preview/`. Derive it
from staging with `forge env new preview --from staging` and fill its
placeholders. It is a real env too: `forge env deploy preview` deploys it like
any other. If your generated `deploy.yml` should not deploy it, list the envs
it should deploy in `forge.yaml` `deploy.environments`.

- **Hosted** (`forge.OnHosted`): nothing else is needed. Each instance becomes
  its own control-plane environment, with its own URL, database and secrets.
  Secrets are per env: give the template's secrets to each instance with
  `forge secret set --env <instance>`, or keep previews on config that needs no
  secrets.
- **A cluster you operate**: give each preview its own namespace by deriving
  it from the env name, which forge binds on every render:

  ```kcl
  _cluster = forge.ClusterTarget {
      cluster = "gke_acme_us-central1_previews"
      namespace = "shop-" + option("env")      # shop-preview-pr-123
      platform = "amd64"
  }
  ```

  `create` renders the instance and **refuses** one that would deploy into a
  namespace the template env itself renders into.

## Naming

`forge env preview name --pr 123` prints the instance name:
`preview-pr-123`. `--branch feat/Login_Page` gives `preview-feat-login-page`.
`--name-template` changes the shape (`{env}` is `--from`, `{id}` is
`pr-<n>` or the branch). A name is a valid env name of at most 40 bytes. A
longer one is cut and gets a 6-hex hash suffix, so two long branches never
collide. The name depends only on the key, so the job that creates a preview
and the job that deletes it agree with no shared state.

## What `create` writes

- It copies the template's files verbatim into `deploy/kcl/<instance>/`, and
  writes two more files:
  - `.forge-preview.json`, the marker: `of`, `expires_at`, `source`.
  - `README.preview`.
- forge binds the marker on **every** render of the instance as
  `-D forge_preview_*`, which becomes `Bundle.preview` (`forge.Preview`):
  - It is recorded in the release shape (`preview`).
  - It is stamped on each of the instance's namespaces as the label
    `forge.dev/preview-of=<template>`, the label
    `forge.dev/preview=<instance>`, and the annotation
    `forge.dev/expires-at=<RFC 3339>`.
- Re-running `create` for the same key refreshes the copy and the expiry.
- `create` refuses to overwrite an existing directory that is not a preview of
  the same template.
- Do not commit instances. A CI checkout creates them, and generated workflows
  never list them.

## TTL and cleanup

`--ttl` is a duration (`72h`) or whole days (`7d`). It defaults to 72h and can
be at most 30d.

| where it runs | who deletes it once it expires |
|---|---|
| hosted | the control plane, when it advertises `shape-preview`: the env and its data, like `forge env delete`. `forge env gc` also does, for an instance whose directory it has |
| a cluster you operate | `forge env gc`: it lists namespaces labelled `forge.dev/preview-of=<template>` on every cluster the template renders into (plus `--context`), and deletes those whose `expires-at` has passed |

`forge env gc --dry-run` lists without deleting. An expiry that cannot be read
is never treated as expired. `forge env preview delete` deletes a namespace
only while it still carries the preview's label.

## CI recipe (GitHub Actions)

Save this as `.github/workflows/preview.yml`. It is yours: forge does not
generate it. It needs the same credentials as `deploy.yml`:
`FORGE_CONTROL_PLANE_TOKEN` for hosted, `KUBECONFIG` for a cluster.

```yaml
name: preview
on:
  pull_request:
    types: [opened, synchronize, reopened, closed]
  schedule:
    - cron: "17 * * * *"          # hourly: delete expired previews
concurrency:
  group: preview-${{ github.event.pull_request.number || 'gc' }}
  cancel-in-progress: false
permissions:
  contents: read
  pull-requests: write
jobs:
  up:
    if: github.event_name == 'pull_request' && github.event.action != 'closed'
    runs-on: ubuntu-latest
    env:
      FORGE_CONTROL_PLANE_TOKEN: ${{ secrets.FORGE_CONTROL_PLANE_TOKEN }}
    steps:
      - uses: actions/checkout@v4
      - run: npm i -g @reliantlabs/forge
      - id: preview
        run: |
          forge env preview create --pr ${{ github.event.pull_request.number }} --ttl 72h --json > preview.json
          echo "name=$(jq -r .name preview.json)" >> "$GITHUB_OUTPUT"
      - run: forge env deploy ${{ steps.preview.outputs.name }} --yes --json > deploy.json
      - run: forge obs check ${{ steps.preview.outputs.name }} --wait --json || true
      - name: Comment the URL
        env:
          GH_TOKEN: ${{ github.token }}
        run: |
          urls=$(forge env status ${{ steps.preview.outputs.name }} --json | jq -r '[.. | .url? // empty] | unique | join("\n")')
          gh pr comment ${{ github.event.pull_request.number }} --edit-last --create-if-none \
            --body "Preview \`${{ steps.preview.outputs.name }}\` (expires $(jq -r .expires_at preview.json)):
          ${urls}"
  down:
    if: github.event_name == 'pull_request' && github.event.action == 'closed'
    runs-on: ubuntu-latest
    env:
      FORGE_CONTROL_PLANE_TOKEN: ${{ secrets.FORGE_CONTROL_PLANE_TOKEN }}
    steps:
      - uses: actions/checkout@v4
      - run: npm i -g @reliantlabs/forge
      - run: forge env preview delete --pr ${{ github.event.pull_request.number }} --yes
  gc:
    if: github.event_name == 'schedule'
    runs-on: ubuntu-latest
    env:
      FORGE_CONTROL_PLANE_TOKEN: ${{ secrets.FORGE_CONTROL_PLANE_TOKEN }}
      # a cluster you operate: write secrets.KUBECONFIG to ~/.kube/config first
    steps:
      - uses: actions/checkout@v4
      - run: npm i -g @reliantlabs/forge
      - run: forge env gc --yes
```

A pull request from a fork gets no secrets. Keep previews to branches in the
repository (`if: github.event.pull_request.head.repo.full_name ==
github.repository`), and never run a fork's code with your deploy token.

## Not yet

- An instance's secrets are not copied from the template.
- A hosted instance is deleted on expiry only by a control plane that
  advertises `shape-preview`. On an older one, schedule `forge env gc` from a
  checkout that recreates the instance, or delete it on PR close.
