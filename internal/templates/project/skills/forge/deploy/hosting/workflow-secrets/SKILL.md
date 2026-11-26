---
name: deploy/hosting/workflow-secrets
description: Grant, inspect and revoke Reliant workflow access to hosted secrets without exposing their values.
---

# Secrets a Reliant workflow uses: access lists

A hosted secret is opaque and write-only, and it carries an **access list**: the
principals allowed to use it. A Reliant workflow uses a secret by declaring it
(top-level `secrets: {NAME: {environment: <project>/<env>}}`) and writing
`{{ secrets.NAME }}` in an `http/request@1` header; the control plane
substitutes the value when the request is sent, so the value never reaches the
workflow, its logs or the model. It does so only if the workflow is on the
list. The list names no destination, path or step: it answers "may this workflow
use this secret", and what the credential can do is the receiving app's call.

```bash
forge secret set    --env staging TRADER_KEY                     # the value, on stdin
forge secret allow  --env staging --workflow trader-bot --project <reliant project id> TRADER_KEY
forge secret access --env staging [TRADER_KEY] [--json]          # who may use it
forge secret deny   --env staging --workflow trader-bot --project <reliant project id> TRADER_KEY
forge secret list   --env staging                                # each secret's access beside its version
```

- `--workflow` is the workflow's name (its `.reliant/workflows/<name>.yaml`);
  any run or trigger of it is covered. `--project` is the Reliant project id
  that owns it: find it with `reliant project list` (the row whose PATH is the
  checkout holding the workflow). forge never guesses it.
- `allow` and `deny` are idempotent. Only an org owner or admin edits a list
  (a machine token with no acting user is refused; a protected env asks for
  step-up). The control plane's refusal is printed as sent.
- A run denied for lack of access names this command in its error.
- Hosted envs only: a `forge.FileSecrets` store has no access list, and these
  commands say so. Workloads keep receiving secrets through `managedSecret` /
  `config_secrets`; access lists govern workflows.
