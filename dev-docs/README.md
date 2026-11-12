# dev-docs

Internal engineering notes: incident write-ups and development process docs.

Nothing here is published. Mintlify serves every file under `docs/` (its
`docs.json` lives there), whether or not `docs.json` links to it, so a page that
is only left out of the nav is still public at its URL. Notes that name chat
IDs, hosts or local infrastructure belong here, not in `docs/`.

- `incidents/` — incident write-ups, referenced by path from code comments.
- `pinning.md` — why reliant pins forge by commit pre-launch, and the commands.
- `forge-upgrade.md` — why reliant always takes the newest forge, the bump procedure, and the log of every bump.
- `action-pins.md` — the reviewed action-to-commit pins in privileged workflows, how to change one, and the live verification after a change.
- `observability.md` — how reliant's own server binaries export traces, metrics and logs, and the known gaps.
- `redundant-index-drop-rollout.md` — the post-deploy plan check for the 2026-11-10 `chat_updates`/`messages` index drops, and the fix-forward migration that recreates one.
- `machine-plan-change-rollout.md` — the machine plan change's live check, and how to stop confirmations and reconcile Stripe if it misbehaves.
- `integration-manifests.md` — authoring an integration manifest: the generated JSON Schema, fragment layout, `$ref`, event conformance.
- `backlog.md` — product proposals and deferred designs moved out of the issue tracker (2026-11-04), each under its original issue number.
- `human-only-approvals-verification.md` — post-deploy check that real app sessions and OAuth-client tokens classify correctly for human-only approvals.
- `sdk-approval-decisions-rollout.md` — post-deploy check of the Go SDK's approval decisions against a real server, and their rollback.
- `forge-shell-rollout.md` — activating the forge shell host (forge.reliantlabs.io): external configuration, post-deploy checks, rollback.
- `project-machines-rollout.md` — post-deploy check of machine-specific folder browsing, project creation and the checkout remove/reclone guard, and their rollback.
- `dashboard-postgres-panels.md` — post-deploy check for dashboard postgres panels (`AppDatabaseService`), and how to roll them back.
- `forge-analytics-pin-rollout.md` — post-deploy check of the forge@cdac1da2 built-in analytics dashboards and the demo's full analytics tier (DNT/GPC included), and their rollback.
- `design/mobile-chat-redesign.md` — mobile and tablet chat thread redesign: one top bar, inline composer, status slot, parity map.
- `design/terminal-reattach.md` — keeping a terminal's shell across a reconnect: attach, detached-session reaper, offset replay (proposal).
