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
- `cli-release-artifacts.md` — the standalone `reliant` CLI archives, their Sigstore signature and the public `daemon-channels.json` manifest each release tag publishes: schema, verification, pipeline and first-release check.
- `observability.md` — how reliant's own server binaries export traces, metrics and logs, and the known gaps.
- `observability-attributes.md` — every span name and attribute reliant's agent and workflow telemetry emits (run, node, agent turn, LLM call, tool call, `run.exec`, `integration.call`, `trigger.fire`), the rules a span follows (`org.id`, opaque ids, no content or error text), how to add one, and what tenant export relies on.
- `redundant-index-drop-rollout.md` — the post-deploy plan check for the 2026-11-10 `chat_updates`/`messages` index drops, and the fix-forward migration that recreates one.
- `inbox-firings-rollout.md` — post-deploy ListInbox latency check for reliant#2736's bounded Inbox reads and `idx_chats_daemon_blocked`, its failure criteria, and the application rollback (the index stays).
- `chat-list-live-index-rollout.md` — post-deploy check that reliant#2945's `idx_chats_user_live_last_active` built valid and ListChats adopts it, the fix-forward for a failed build, and the application rollback (the index stays).
- `machine-plan-change-rollout.md` — the machine plan change's live check, and how to stop confirmations and reconcile Stripe if it misbehaves.
- `pr-records.md` — PR records: webhook-fed pull request state with head-bound checks (the PR-pipeline primitive): data model, event transitions, API, reconciliation, the shadow and flip plan, and the owner decisions.
- `workflow-checks-rollout.md` — post-release smoke check of the GitHub check runs a workflow's `check:` block writes (pass, fail, refused before launch), how to stop the writes, and how to conclude checks left queued or in progress.
- `integration-manifests.md` — authoring an integration manifest: the generated JSON Schema, fragment layout, `$ref`, event conformance.
- `backlog.md` — product proposals and deferred designs moved out of the issue tracker (2026-11-04), each under its original issue number.
- `human-only-approvals-verification.md` — post-deploy check that real app sessions and OAuth-client tokens classify correctly for human-only approvals.
- `sdk-approval-decisions-rollout.md` — post-deploy check of the Go SDK's approval decisions against a real server, and their rollback.
- [Forge identity cutover](forge-identity-cutover.md) — operator evidence checklist for switching sign-in and, separately, retiring Supabase for the managed app.
- `desktop-sourcemaps.md` — how a desktop release files its renderer's source maps, the `release` environment's `RELIANT_SOURCEMAPS_TOKEN`, and how to set it up and check it.
- `preview-env.md` — the hosted preview: the production web build on the shop fixture pack (forge.Fixtures), hosted as a control-plane prod frontend; the tests that prove its sign-in code is inert, and its noindex.
- `forge-shell-rollout.md` — activating the forge shell host (forge.reliantlabs.io): external configuration, post-deploy checks, rollback.
- `project-machines-rollout.md` — post-deploy check of machine-specific folder browsing, project creation and the checkout remove/reclone guard, and their rollback.
- `dashboard-postgres-panels.md` — post-deploy check for dashboard postgres panels (`AppDatabaseService`), and how to roll them back.
- `forge-analytics-pin-rollout.md` — post-deploy check of the forge@cdac1da2 built-in analytics dashboards and the demo's full analytics tier (DNT/GPC included), and their rollback.
- `design/mobile-chat-redesign.md` — mobile and tablet chat thread redesign: one top bar, inline composer, status slot, parity map.
- `design/terminal-reattach.md` — keeping a terminal's shell across a reconnect: attach, detached-session reaper, offset replay (proposal).
