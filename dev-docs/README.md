# dev-docs

Internal engineering notes: incident write-ups and development process docs.

Nothing here is published. Mintlify serves every file under `docs/` (its
`docs.json` lives there), whether or not `docs.json` links to it, so a page that
is only left out of the nav is still public at its URL. Notes that name chat
IDs, hosts or local infrastructure belong here, not in `docs/`.

- `incidents/` — incident write-ups, referenced by path from code comments.
- `pinning.md` — why reliant pins forge by commit pre-launch, and the commands.
- `observability.md` — how reliant's own server binaries export traces, metrics and logs, and the known gaps.
- `integration-manifests.md` — authoring an integration manifest: the generated JSON Schema, fragment layout, `$ref`, event conformance.
- `backlog.md` — product proposals and deferred designs moved out of the issue tracker (2026-11-04), each under its original issue number.
- `human-only-approvals-verification.md` — post-deploy check that real app sessions and OAuth-client tokens classify correctly for human-only approvals.
- `dashboard-postgres-panels.md` — post-deploy check for dashboard postgres panels (`AppDatabaseService`), and how to roll them back.
- `design/mobile-chat-redesign.md` — mobile and tablet chat thread redesign: one top bar, inline composer, status slot, parity map.
