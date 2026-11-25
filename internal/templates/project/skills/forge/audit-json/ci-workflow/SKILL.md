---
name: ci-workflow
description: Drop-in GitHub Actions workflow that runs `forge project audit --json` + `forge project map --json`, uploads them as artifacts, fails on error status, and posts a PR summary comment.
---

# Audit JSON in CI

A drop-in workflow that runs both commands, uploads the JSON as
artifacts, and posts a summary comment on PRs. Read the `audit-json` skill for
the JSON shapes and status semantics this workflow keys off.

```yaml
# .github/workflows/forge-audit.yml (Tier-2 — user-owned)
# yours: scaffolded once, never touched again — forge will not overwrite this file
name: Forge Audit
on:
  pull_request:
  push:
    branches: [main]

jobs:
  audit:
    runs-on: ubuntu-latest
    steps:
      - uses: actions/checkout@v4
      - uses: actions/setup-go@v5
        with:
          go-version-file: go.mod
      - name: Install the forge CLI
        # The version go.mod pins (the one that generated this project), not
        # @main, which audits with whichever release is newest.
        run: |
          v=$(GOWORK=off go list -m -f '{{.Version}}' github.com/reliant-labs/forge)
          go install "github.com/reliant-labs/forge/cmd/forge@${v}"

      - name: Forge audit
        id: audit
        run: |
          forge project audit --json > audit.json
          forge project map --json   > map.json
          echo "status=$(jq -r .overall_status audit.json)" >> "$GITHUB_OUTPUT"
          echo "drift_count=$(jq '[.. | select(.flags? // [] | index("drift"))] | length' map.json)" >> "$GITHUB_OUTPUT"

      - name: Upload artifacts
        uses: actions/upload-artifact@v4
        with:
          name: forge-audit
          path: |
            audit.json
            map.json

      - name: Fail on error
        if: steps.audit.outputs.status == 'error'
        run: |
          echo "::error::forge project audit reported overall_status=error"
          jq '.categories | to_entries[] | select(.value.status == "error")' audit.json
          exit 1

      - name: Comment summary
        if: github.event_name == 'pull_request'
        uses: actions/github-script@v7
        with:
          script: |
            const audit = require('./audit.json');
            const drift = '${{ steps.audit.outputs.drift_count }}';
            const lines = ['### Forge audit',
              `**Overall:** \`${audit.overall_status}\` (binary ${audit.binary_version})`,
              `**Drifted files:** ${drift}`,
              '',
              '| Category | Status | Summary |',
              '|----------|--------|---------|',
              ...Object.entries(audit.categories).map(
                ([k, v]) => `| ${k} | \`${v.status}\` | ${v.summary} |`)];
            github.rest.issues.createComment({
              issue_number: context.issue.number,
              owner: context.repo.owner,
              repo:  context.repo.repo,
              body:  lines.join('\n'),
            });
```

Drop the file at `.github/workflows/forge-audit.yml`. `forge generate`
won't touch it (the file name isn't on forge's Tier-1 list).

Gate on `overall_status == "error"` only; `warn` is informational and would
otherwise block work on soft drift.

## When this skill is not enough

- **The audit/map JSON shapes, categories, flags, jq queries** — see `audit-json`.
- **The CI workflows forge generates itself** (and the Tier-1/Tier-2 boundary) —
  see `ci`.
