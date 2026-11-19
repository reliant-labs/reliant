---
name: v1.0.10
description: Upgrade a project to forge v1.0.10. .forge/hashes.json becomes one certificate file per JSON output under .forge/hashes/, and contract mocks (mock_gen.go) certify each mock separately, so pull requests that change different outputs or interfaces no longer conflict in generated files. Use when the project commits .forge/hashes.json.
version: v1.0.10
detection: test -f .forge/hashes.json && exit 0; exit 1
---

# Upgrading to v1.0.10

The first `forge generate` on v1.0.10 migrates everything below. Commit what it writes in one change.

## 1. `.forge/hashes.json` becomes `.forge/hashes/`

**Use when** the project commits `.forge/hashes.json`.

**What changed.** A JSON output (observability dashboards and specs, the config schema) cannot carry the embedded `forge:hash` header, so its certificate, the hash of forge's last render, lived in one shared `.forge/hashes.json`. Every branch that re-rendered any JSON output edited that file, and two such branches conflicted in it even when the outputs did not. Each certificate is now its own file, at the output's path under `.forge/hashes/` with `.sha256` appended:

```
deploy/observability/spec/alert-service-chat_gen.json
.forge/hashes/deploy/observability/spec/alert-service-chat_gen.json.sha256
```

Two branches now conflict there only when both changed the same output.

**What generate does.** It reads the old file, writes one certificate per entry, deletes `.forge/hashes.json`, and rewrites `.gitignore`'s `!.forge/hashes.json` to `!.forge/hashes/`. The `.gitignore` line is rewritten only when it is exactly forge's own. If your `.gitignore` re-includes `.forge` state some other way, re-include `.forge/hashes/` yourself, or every clone will see its JSON outputs as uncertified. A `.gitattributes` copied from forge's scaffold may still name `.forge/hashes.json merge=forge-generated`: replace that line with `.forge/hashes/** merge=forge-generated`. forge's merge driver writes the new route into the clone's own attributes either way.

## 2. Mocks certify each interface separately

**Use when** the project has contract packages (`internal/<pkg>/mock_gen.go`).

**What changed.** `mock_gen.go` carried one `forge:hash` over the whole file, so two branches that changed different interfaces of one package conflicted on that one header line. Each mock now begins with its own `// forge:section forge:hash=…` line, which certifies that mock alone. The header hash covers only the package clause and imports. A file is pristine only when every hash verifies, so hand-edit detection is unchanged. Two branches that change different interfaces now merge with plain git into what `forge generate` writes for both.

**What generate does.** It rewrites every `mock_gen.go`. Mock names and behaviour are unchanged. Nothing to do by hand.
