# Forge identity cutover: readiness checklist

This checklist records evidence, not permission to deploy, export accounts, or
collect credentials. Use a disposable non-production environment and synthetic
accounts for rehearsals. Keep production sign-in on Supabase until the owner
approves the switch against the evidence below. The operator identity (tenant
zero) is not a substitute test target for Reliant's end-user identity.

## What the existing proofs cover

| Evidence on main | What it proves | What it does not prove |
| --- | --- | --- |
| [Reliant broker smoke](../.reliant/tools/signin-e2e.sh) | Local registration, server-side cookie session, one authenticated RPC, the approval gate, and logout. Run it only against the worktree's own host-only dev stack, with no production URL. | Hosted routing, social sign-in, or imported accounts. |
| [Forge's Zitadel social test](https://github.com/reliant-labs/forge/blob/main/internal/cli/social_signin_e2e_test.go) and `reliant forge cloud identity status --check-social` | Sign-in starts for Google and GitHub, and the authorize URL names the expected callback. The test also exercises refusal paths. | Completing consent at either provider, the successful return to the broker, or access to GitHub repos after sign-in. |
| [Forge's synthetic import test](https://github.com/reliant-labs/forge/blob/main/internal/cli/identity_import_e2e_test.go) | Zitadel preserves the subject, accepts the original bcrypt password, preserves verified email and the GitHub link, writes nothing on dry run, and re-imports unchanged rows without rewriting them. | An end-to-end Supabase-to-Reliant import or a successful upstream sign-in on an imported account. |

The broker, control-plane session resolver client, hosted import API and
flagged web and desktop clients have landed. This is not a release verdict:
[`AUTH_PROVIDER`](../proto/reliant/config/v1/web_config.proto) defaults to
`supabase`, the [Reliant identity declaration](../deploy/kcl/identity.k) is wired
for dev, and [control-plane prod](https://github.com/reliant-labs/control-plane/blob/main/deploy/kcl/prod/main.k)
currently binds the operator identity, not the separate Reliant end-user one.
The [hosted import gate](https://github.com/reliant-labs/control-plane/blob/main/internal/deploy/identity_tenant_zero_render_test.go)
is still off in prod.

## Evidence required before switching sign-in

Record the tested release identifiers, environment, date, and pass/fail for
each item in the authorized rollout record. Record aggregate counts and failure
categories only; do not attach passwords, hashes, session cookies, tokens,
OAuth traces, account identifiers, or export rows.

- [ ] **R4 — Separate end-user identity.** Verify the approved declaration
  and control-plane binding: open signup, password, magic link, passkey for
  `reliantlabs.io`, platform mail, and Google and GitHub. Confirm that issuer,
  audience, JWKS and broker delivery belong to the end-user instance; the
  operator API must still refuse those users. Verify the public social callback
  and the mail delivery path independently.
- [ ] **R4 — Two live provider round trips.** With an authorized non-production
  OAuth app and a synthetic account for each provider, complete consent at
  **Google** and **GitHub**, return through the real provider callback and
  broker, and verify that the resulting server-side session can call a
  protected RPC. For GitHub, also verify that sign-in yields working repo access
  for a test repository without a second sign-in. The authorize-URL check is a
  prerequisite, not a substitute for either round trip.
- [ ] **R5 — Account import, not just an importer.** First rehearse dry run,
  apply, and an idempotent re-run using synthetic source-format rows on the
  intended non-production path. Prove the same subject and existing Reliant
  account/data after sign-in, original password, verified email, and both social
  links resolving to that subject rather than a duplicate account. Before a
  production switch, authorized operators must reconcile source and target
  counts, classify import errors and link conflicts, and prove the final delta
  catches changes since the initial import. Leave the gate open if rows or
  sign-in samples are unexplained. No live export or import is part of this
  checklist's rehearsal.
- [ ] **R3/R6/R7 — Product routes and supported clients.** In Forge mode, prove
  both browser hosts can sign in through the named same-origin routes
  (`/_api/@reliant` and `/_api/@cp`). Exercise onboarding with an existing
  control-plane account, CLI and device approval, and workspace proxy sessions.
  Verify that a live server-side cookie session can decide approvals but a
  bearer or an `rlat_` token cannot; keep the self-host shared-key path. Prove
  system-browser handoff, sign-out, and daemon startup on supported packaged
  desktop platforms. Confirm existing CLI, SDK and daemon `rlat_` credentials
  still reach the endpoints they did before the switch.
- [ ] **R8 — Approved change and rollback.** Record the final delta result,
  the expected one-time re-sign-in (no silent token exchange), and the plan for
  guests, which the managed app does not migrate as guest sessions. Prove
  rollback preserves accounts created during the transition. Flipping
  `AUTH_PROVIDER` back is not safe if any new account exists only in Zitadel.
  Keep Supabase available until the approved rollback window closes; record the
  sign-in funnel, auth error rate, and release-health verdict before closing it.

## When Supabase can be retired for the managed app

Switching sign-in is not the same as removing Supabase validation. Confirm that
both web hosts are fully on Forge mode and the minimum supported desktop release
can sign in to Forge. Prove existing MCP clients can reconnect through the
control-plane OAuth issuer for **both** the workspace and Forge connectors
(R9), with no Supabase fallback traffic still required. Use the issuer-labelled
[`reliant_auth_credentials_total`](../internal/auth/authmetrics/authmetrics.go)
series (`source="supabase"`) to show approximately zero Supabase traffic over
14 days; absent or stale telemetry does not count as zero. Only then seek the
owner's sign-off to retire the managed Supabase path (R10). This checklist does
not retire or change self-host auth.
