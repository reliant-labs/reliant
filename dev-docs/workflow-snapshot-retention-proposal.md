# Workflow snapshot retention — proposal, not implemented

Bug report: 0deafd48. This follow-up requires the owner's policy decision (question 237bf782). The nested-discovery fix does not change snapshot retention, ownership, or deletion semantics.

## Confirmed incident

Both merge-steward activations referenced project d70701a6-8aa9-4fb8-8d13-9c41b708201d, `/home/workspace/projects/reliant-labs`. Read-only production inspection found its config replaced at 2026-10-26 23:09:54 UTC by machine `cicd` (`e164d47d-1288-468d-ad0b-c487af2da1c4`, suspended), with `project_workflows_json` NULL. Machine `default` (`2aab1465-f76b-4d95-b61b-480d9ba070e6`) had the root workflow on disk. The watcher suppresses snapshots whose content hash equals the last one it sent, not the server's current version. Thus default's unchanged snapshot did not repair cicd's overwrite.

A comment-only save to the existing root YAML induced a normal sync. Both activations changed from BROKEN (current definition unresolved) to FAILING (historical failed firings). No activation was enabled, repointed, recreated, or mutated.

## Current data model

`project_configs` has one row per project. A daemon snapshot replaces config YAML and MCP settings (user/project/local), memories (global/project/nested repos), workflows, presets, scenarios, skills, runtime metadata and timestamps. It stores YAML server-side already. Disconnect/removal does not itself delete the row. The lost YAML and its original provenance cannot be recovered from the overwritten row.

## Alternatives awaiting approval

1. **Source-owned workflow files (recommended):** retain each file's last synced YAML plus machine name/ID and source path. Another machine's omission cannot delete it. A present file updates/takes ownership; omission on its last source creates a tombstone with diagnostic provenance. Scope this to workflows, not silently to MCP, skills or memories. Serialize read/merge/write per project. Define scenario retention alongside workflows so files and tests cannot diverge.
   - Benefit: small, bounded change without independent per-machine catalogs.
   - Trade-off: last supplier owns deletion; branch/worktree differences can take ownership, and stale copies can resurrect a deleted definition. Those semantics need explicit owner acceptance and tests, not an implementation hidden inside a discovery fix.
2. **Per-machine catalogs with explicit authoritative selection:** preserve independent snapshots and select the workflow source in activation/project settings.
   - Benefit: no implicit ownership transfer.
   - Cost: schema, lifecycle, selection UI and conflict policy; broader redesign.
3. **Defer retention:** ship discovery and honest source diagnostics first. Current replacement semantics remain documented; reconnect/save repairs the catalog but is not a durable prevention mechanism.

No retention implementation ships with this proposal. Snapshot resend acknowledgements/periodic anti-entropy could repair delivery loss but are not a substitute for deciding which machine is authoritative; blindly resending competing catalogs would just oscillate.
