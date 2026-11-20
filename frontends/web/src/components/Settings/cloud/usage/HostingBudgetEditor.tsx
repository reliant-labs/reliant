// Copyright (c) 2026 Reliant Labs

/**
 * The org's monthly app hosting budget once its agreement is active: change it
 * in place, and see every version it has had.
 *
 * ── WHY NO STRIPE ROUND TRIP ───────────────────────────────────────────────
 *
 * The budget is an admission gate (new deploys pause when accrued spend
 * reaches it), not an invoice cap, and Stripe never sees it. So a change is
 * recorded in-app as an append-only version: who, when, the amount, and the
 * consent to the usage policy that version was made under. It applies at
 * once, which is why lowering it below what the month has already spent says
 * plainly that new deploys pause immediately.
 *
 * The older per-user deploy cap (SetCurrentUserInfraOverage) is NOT offered
 * here or anywhere for hosting: one budget, the org's.
 */

import { useId, useMemo, useState } from "react";
import { Code, ConnectError } from "@connectrpc/connect";
import { timestampDate } from "@bufbuild/protobuf/wkt";

import Button from "@/components/forge-ui/button";
import Input from "@/components/forge-ui/input";
import { ConsentCheckbox } from "@/components/Billing/ConsentCheckbox";
import { formatCents } from "./usageModel";
import {
  isUnimplemented,
  useSetUsageBillingBudget,
  useUsageBillingBudgetVersions,
  type UsageBillingBudgetVersion,
} from "./hostingBillingQueries";
import { HOSTING_BUDGET_MAX_LABEL, hostingBudgetInputValue, validateHostingBudget } from "./hostingBudget";

export interface HostingBudgetEditorProps {
  orgId: string;
  /** The effective budget, as the agreement reports it. */
  budgetCents: bigint;
  /** The policy version the server currently offers; each change consents to it. */
  policyVersion: string;
  canManage: boolean;
  /** Accrued hosting spend this period, when known: lowering below it pauses deploys at once. */
  spentCents?: number | null;
}

export function HostingBudgetEditor({ orgId, budgetCents, policyVersion, canManage, spentCents }: HostingBudgetEditorProps) {
  const versions = useUsageBillingBudgetVersions(orgId);
  const save = useSetUsageBillingBudget(orgId);
  const [editing, setEditing] = useState(false);
  const [draft, setDraft] = useState("");
  const [accepted, setAccepted] = useState(false);
  const [message, setMessage] = useState<string | null>(null);
  const [problem, setProblem] = useState<string | null>(null);
  const fieldId = useId();

  const history = useMemo(
    () => [...(versions.data?.versions ?? [])].sort((a, b) => Number(b.version) - Number(a.version)),
    [versions.data],
  );
  const current = history.find((v) => v.current) ?? history[0];
  const unserved = isUnimplemented(versions.error);
  const { cents: draftCents, error: draftError } = validateHostingBudget(draft);
  const pausesNow = draftCents !== undefined && spentCents != null && Number(draftCents) <= spentCents;

  const start = () => {
    setDraft(hostingBudgetInputValue(budgetCents));
    setAccepted(false);
    setMessage(null);
    setProblem(null);
    setEditing(true);
  };

  const submit = () => {
    if (draftError !== undefined) return setProblem(draftError);
    if (!accepted) return setProblem("Agree to the usage billing policy to change the budget.");
    if (!policyVersion) return setProblem("The usage billing policy could not be verified. Reload and try again.");
    setProblem(null);
    save.mutate(
      {
        infraBudgetCents: draftCents,
        policyVersion,
        consent: true,
        expectedCurrentVersion: current ? BigInt(current.version) : undefined,
      },
      {
        onSuccess: () => {
          setEditing(false);
          setMessage(`Budget set to ${formatCents(Number(draftCents))} a month. It applies now.`);
        },
        onError: (error) => setProblem(saveErrorMessage(error)),
      },
    );
  };

  return (
    <div className="space-y-3" data-testid="hosting-budget-editor">
      <div className="flex flex-wrap items-baseline justify-between gap-2">
        <p className="text-ui text-foreground">
          Monthly budget: <span className="font-medium tabular-nums">{formatCents(Number(budgetCents))}</span>
        </p>
        {canManage && !editing && !unserved && (
          <Button size="sm" variant="outline" onClick={start} data-testid="hosting-budget-change">
            Change budget
          </Button>
        )}
      </div>
      <p className="text-meta text-muted-foreground">
        When this month&apos;s hosting spend reaches the budget, new deploys pause. Apps already running keep running and can add a little more before
        enforcement catches up, so it isn&apos;t a hard cap on your bill. Stored data is never deleted.
      </p>
      {unserved && canManage && (
        <p role="status" className="text-meta text-muted-foreground" data-testid="hosting-budget-unserved">
          Changing the budget isn&apos;t available on this server yet.
        </p>
      )}
      {!canManage && <p className="text-meta text-muted-foreground">Only an organization owner or admin can change the budget.</p>}
      {message && (
        <p role="status" className="text-ui text-foreground">
          {message}
        </p>
      )}

      {editing && (
        <form
          className="space-y-3 rounded-xl border border-border/60 bg-background p-4"
          onSubmit={(event) => {
            event.preventDefault();
            submit();
          }}
        >
          <div className="forge-ui space-y-1.5">
            <label htmlFor={fieldId} className="block text-ui font-medium text-foreground">
              New monthly budget (USD)
            </label>
            <Input id={fieldId} inputMode="decimal" value={draft} onChange={(e) => setDraft(e.target.value)} invalid={draft !== "" && draftError !== undefined} aria-describedby={`${fieldId}-help`} />
            <p id={`${fieldId}-help`} className="text-ui text-muted-foreground">
              Self-serve maximum for budget changes: {HOSTING_BUDGET_MAX_LABEL} per month.
            </p>
          </div>
          {pausesNow && (
            <p className="text-meta text-warning-ink" data-testid="hosting-budget-pauses-now">
              This month has already spent {formatCents(spentCents ?? 0)}, so new deploys pause as soon as you save.
            </p>
          )}
          <ConsentCheckbox checked={accepted} onChange={(e) => setAccepted(e.target.checked)}>
            I agree to pay for this organization&apos;s app hosting under the usage billing policy, with this monthly budget. It applies immediately.
          </ConsentCheckbox>
          {problem && (
            <p role="alert" className="text-meta text-destructive-ink">
              {problem}
            </p>
          )}
          <div className="flex gap-2">
            <Button type="submit" size="sm" variant="primary" loading={save.isPending} disabled={save.isPending}>
              Save budget
            </Button>
            <Button type="button" size="sm" variant="ghost" onClick={() => setEditing(false)}>
              Cancel
            </Button>
          </div>
        </form>
      )}

      {!unserved && <BudgetHistory versions={history} loading={versions.isLoading} failed={!!versions.error} onRetry={() => void versions.refetch()} />}
    </div>
  );
}

function saveErrorMessage(error: unknown): string {
  if (error instanceof ConnectError) {
    if (error.code === Code.Unimplemented) return "Changing the budget isn't available on this server yet.";
    // FAILED_PRECONDITION is either a stale expected_current_version ("budget
    // changed") or an agreement that is no longer active.
    if (error.code === Code.Aborted || (error.code === Code.FailedPrecondition && /budget changed/i.test(error.rawMessage)))
      return "The budget changed since you opened this. Review the latest version below, then try again.";
    if (error.code === Code.FailedPrecondition) return "This organization's hosting agreement isn't active, so its budget can't be changed. Check billing status.";
    if (error.code === Code.InvalidArgument) return "The usage billing policy changed. Reload this page, review it, and agree again.";
    if (error.code === Code.PermissionDenied) return "Only an organization owner or admin can change the budget.";
  }
  return "The budget could not be saved. Nothing changed; try again.";
}

function BudgetHistory({
  versions,
  loading,
  failed,
  onRetry,
}: {
  versions: UsageBillingBudgetVersion[];
  loading: boolean;
  failed: boolean;
  onRetry: () => void;
}) {
  if (loading) return <p className="text-meta text-muted-foreground">Loading budget history…</p>;
  if (failed)
    return (
      <p className="text-meta text-muted-foreground">
        Budget history could not be loaded.{" "}
        <button type="button" className="text-primary-ink hover:underline" onClick={onRetry}>
          Try again
        </button>
      </p>
    );
  if (versions.length === 0) return null;
  return (
    <div>
      <h4 className="mb-1 text-ui font-medium text-foreground">Budget history</h4>
      <ul className="divide-y divide-border-subtle" data-testid="hosting-budget-history">
        {versions.map((v) => (
          <li key={String(v.version)} className="flex flex-wrap items-baseline justify-between gap-x-3 py-1.5 text-ui">
            <span className="tabular-nums text-foreground">
              {formatCents(Number(v.infraBudgetCents))}
              {v.current && <span className="ml-2 text-meta text-muted-foreground">current</span>}
            </span>
            <span className="text-meta text-muted-foreground">
              {v.source === "checkout_consent" ? "Set at checkout" : "Changed"} by {v.actorEmail || "a former member"}
              {v.createdAt ? ` · ${timestampDate(v.createdAt).toLocaleString()}` : ""}
            </span>
          </li>
        ))}
      </ul>
    </div>
  );
}
