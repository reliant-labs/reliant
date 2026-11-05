# Machine plan change: live check, containment and rollback

The plan-change panel (`frontends/web/src/components/Billing/MachinePlanChange.tsx`)
is the product's only client of control-plane's `PreviewMachinePlanChange` /
`ChangeMachinePlan` (design: control-plane
`docs/design/guaranteed-machine-repricing.md` §11). Those RPCs move real
money, so the post-merge check below comes with what to do when it goes wrong.

## Before the panel ships: the paired control-plane must hold #1935

The panel's "Check again" retry and its "nothing was charged" wording are
safe only against a control-plane that contains control-plane#1935 (merge
commit `71b62c0f0008b1232b7cbf3ab8888896425d85ee`: per-attempt Stripe
idempotency, webhook-race and late-attempt recovery). An older server can
charge a retried confirmation twice. Before the release that carries this
panel is promoted to an environment, the person promoting it checks both, in
a control-plane checkout with `origin` fetched:

1. **The paired server contains the fix.** Take the control-plane revision
   that environment runs (the release tag being deployed with it, or the
   running image's revision) and run
   `git merge-base --is-ancestor 71b62c0f0008b1232b7cbf3ab8888896425d85ee <revision>`.
   Exit 0 passes; anything else fails. (When this panel merged, the open
   candidate `release/v1.7.88-rc.7` did not contain it and
   `release/v1.7.96-rc.1` did, so the check is not a formality.)
2. **The fix's integration suite ran green on it.** `scripts/ci-full-main.sh`
   must show a PASS record for that revision or an ancestor that descends from
   `71b62c0f` (`scripts/cut-release.sh` already refuses a cut without one).
   That run is `task ci:full`, whose integration lane runs the
   `//go:build integration` files #1935 added to:
   `internal/billing/svcbilling/machine_plan_change_integration_test.go`
   (`TestMachinePlanChangeRetryNeverChargesTwice`,
   `TestMachinePlanChangeWebhookRaceRecordsOnce`,
   `TestMachinePlanChangeAppliedAfterClose*`,
   `TestMachinePlanChangeClosedAttempt*`,
   `TestMachinePlanChangeSupersededChargedAttemptIsNeverSettledAsUnapplied`)
   and `internal/billing/daemonmeter/coupon_drain_integration_test.go`.
   A cut made with `--skip-full-gate` does not pass this check.

If either check fails: **hold this repo's release** for that environment
(do not promote it; pair it with a control-plane release that passes), or, if
it already shipped, revert this PR's merge commit as in "Stop further
confirmations" below. Record the revision and the record's sha on the PR.

## Live check (Stripe test mode, after the release ships)

On an org with an M machine subscription in a test-mode environment:

1. Upgrade M → L. Record the preview's amount due today, then the paid
   invoice (`billing_reason=subscription_update`): the two must be equal.
   Confirm the `controlplane.machine_plan_changes` row is `applied` with that
   `stripe_invoice_id`, and the period's `machine_allowance_adjustments` row
   keyed `plan-change:<change id>` exists.
2. Press confirm again (and "Check again" after a forced network failure):
   no second invoice, no second row.
3. Decline: a test card that declines gives a refusal, a `failed` row, no
   invoice, plan unchanged.
4. Downgrade: scheduled for the period end, nothing charged; a downgrade
   blocked by a machine larger than the target is refused with that machine
   named. Withdraw the scheduled downgrade ("keep my plan").

Record invoice ids, change ids and screenshots on the PR.

## If something is wrong: stop first, then reconcile

Signs: a second invoice for one confirmation, a charge that differs from the
preview, an `applied` row with no allowance adjustment (or the reverse), a
`pending` row older than 15 minutes, or a `machine billing hold` error in
control-plane's billing logs.

**1. Stop further confirmations.**

- **Product path (this repo):** revert this PR's merge commit
  (`git revert -m 1 <merge sha>`), open it as a PR labelled `release:asap`.
  The revert is UI-only and safe: subscribers fall back to the earlier
  behaviour (the checkout's "separate next-period terms" notice), which
  cannot create a plan change. No data or migration depends on the panel.
- **Server path:** there is no dedicated control-plane switch for these RPCs;
  an owner/admin can still call `ChangeMachinePlan` directly. If a server
  defect is the cause, a person decides on a control-plane change (revert or
  a refusal in `planChangeStore`); do not unset the Stripe configuration,
  which stops all billing, not just plan changes.

**2. Do not retry blindly, and never reverse automatically.** Each attempt
carries its own Stripe idempotency key (`machine-plan-change:<change id>`), so
"Check again" on the same quote cannot charge twice; a *new* confirmation is a
new attempt. Let `pending` rows settle: the webhook and the
`machine_pricing_reconcile` worker finalize or close them within 15 minutes.
No code path refunds or reverses a change at Stripe on its own, by design
(§11.4, "Never compensate automatically").

**3. Reconcile each affected org by hand**, read-only first:

- control-plane DB: `SELECT id, direction, status, amount_due_cents,
  stripe_invoice_id, failure_reason, requested_at FROM
  controlplane.machine_plan_changes WHERE org_id = '<org>' ORDER BY
  requested_at;` and the org's `machine_allowance_adjustments` rows keyed
  `plan-change:%`.
- Stripe: the subscription's `subscription_update` invoices, and the
  subscription's `machine_plan_change_id` metadata.
- Match every paid invoice to exactly one `applied` row. For an invoice with
  no row, or a charge that differs from the confirmed quote, a person decides
  the remedy (a refund in the Stripe dashboard, and a matching allowance
  correction) and records it on the incident. Leave the ledger rows as they
  are until that decision: the renewal reconciler holds the org's grant while
  Stripe bills a price no acceptance names, which keeps the mismatch visible.
