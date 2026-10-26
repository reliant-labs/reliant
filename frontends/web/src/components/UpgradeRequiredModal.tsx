import { useCallback, useMemo, useState } from "react";
import { useQueryClient } from "@tanstack/react-query";
import { CheckCircle2, Zap } from "lucide-react";

import Modal from "./forge-ui/modal";
import Button from "@/components/forge-ui/button";
import type { UpgradeRequiredData } from "../store/modalStore";
import { useGoToBilling } from "@/hooks/useGoToBilling";
import { useComputeSubscription, usePlans } from "@/hooks/useCloudBillingQueries";
import { LinkIdentityModal } from "./Billing/LinkIdentityModal";
import { ComputeSubscriptionCheckout } from "./Billing/ComputeSubscriptionCheckout";
import { InlineCreditTopup } from "./Billing/InlineCreditTopup";
import { deriveMachineOptions } from "./Billing/machineOptions";
import { isPurchasableComputePlan, sortPlansForDisplay } from "./Settings/cloud/billingUtils";
import {
  ACCOUNT_REQUIRED_MACHINE_COPY,
  ACCOUNT_REQUIRED_REASON,
} from "@/lib/accountRequired";

export interface UpgradeRequiredModalProps {
  isOpen: boolean;
  onClose: () => void;
  data: UpgradeRequiredData;
}

// Canonical reason codes, arriving either as the reason on a
// ResourceExhausted Connect error (see api/upgradeInterceptor.ts) or from the
// chat-error marker path in store/chatStore.ts. Unknown reasons fall through to
// the generic copy below.
//
// THERE IS NO FREE TIER. Every entry here used to say there was, which made a
// user who had simply run out of credit — or who had never bought any — read a
// sentence about a "shared free-tier budget" that stopped existing when
// migration 00062_drop_free_tier_llm_pool dropped the pool it metered. Copy in
// this table must describe the condition that actually tripped, and must not
// contradict the server's own message, which renders verbatim underneath it as
// `data.message`.
const REASON_COPY: Record<string, { title: string; body: string }> = {
  // Per-org wallet at or below zero: control-plane's preflightBillingGate.
  // This is the common case, and the fix is a top-up, not a subscription — so
  // the copy says "add credit" rather than "upgrade".
  reliant_credit_exhausted: {
    title: "You're out of Reliant credit",
    body: "Reliant's models draw on your credit balance, which is now empty. Add credit to keep going — or switch to a provider you already pay for, like Claude or ChatGPT.",
  },
  // The remaining entries mirror internal/svcdaemon/connecterr.go, which
  // documents its reason strings as a stable wire contract for exactly this
  // table. Only ResourceExhausted and InvalidArgument reach this modal via
  // upgradeInterceptor, but the copy is kept complete so a code that starts
  // routing here does not silently fall through to GENERIC_COPY.
  //
  // Raised once included minutes AND any redeemed coupon grant are spent, with
  // overage billing off.
  compute_budget_exhausted: {
    title: "You've used the compute on your plan",
    body: "Your included compute minutes and any redeemed coupon time are spent. Upgrade, or enable overage billing, to keep your hosted environment running.",
  },
  // No compute subscription at all — the new-account case. A coupon clears it
  // just as well as a plan, and for most people it is the cheaper answer, so
  // the copy names both.
  no_compute_subscription: {
    title: "Hosted compute isn't set up yet",
    body: "Running a machine in the cloud needs a compute plan. Choose one, or redeem a coupon code if you have one.",
  },
  // The active plan does not permit the requested machine size.
  daemon_size_denied: {
    title: "That machine size isn't on your plan",
    body: "Your current plan doesn't include machines this large. Upgrade to unlock bigger machines, or pick a smaller size.",
  },
  // GrowDaemonDisk's target is past the plan's max_daemon_storage_gib, or the
  // plan offers no disk growth. The Grow disk modal shows this inline today.
  daemon_storage_denied: {
    title: "That disk size isn't on your plan",
    body: "Your current plan doesn't let this machine's disk grow that large. Upgrade for a larger limit, or pick a smaller size.",
  },
};

const GENERIC_COPY = {
  title: "Quota exceeded",
  body: "You've hit a plan limit. Upgrade to continue using this feature.",
};

/**
 * The reasons a compute plan fixes. The modal mounts the compute checkout for
 * these instead of sending the user to billing.
 */
const COMPUTE_REASONS = new Set(["no_compute_subscription", "compute_budget_exhausted", "daemon_size_denied", "daemon_storage_denied"]);

/** The reasons an email on the account fixes: one prompt for both. */
const IDENTITY_REASONS = new Set<string>([ACCOUNT_REQUIRED_REASON, "billing_email_missing"]);

/**
 * A plan limit, a spent budget or an empty wallet stopped what the user was
 * doing. The fix happens HERE, over their work:
 *
 * - a compute reason mounts the compute checkout (the same one billing and
 *   the Add machine sheet use);
 * - credit exhaustion mounts the credit top-up;
 * - a missing email opens the one account prompt, and the call that needed
 *   it is re-run automatically once the account has one — that call
 *   (creating a checkout intent, or similar) charges nothing by itself.
 *
 * After a PAYMENT nothing re-runs on its own. The modal shows one button that
 * repeats the blocked action (`data.retry`, from the interceptor), because the
 * action may itself be billable — starting a machine — and a billable call
 * fires only on a person's click. Without a retry (the chat path resends on
 * its own), the button just closes.
 */
export function UpgradeRequiredModal({
  isOpen,
  onClose,
  data,
}: UpgradeRequiredModalProps) {
  const qc = useQueryClient();
  const goToBilling = useGoToBilling("upgrade");
  const copy = REASON_COPY[data.reason] ?? GENERIC_COPY;
  const [paid, setPaid] = useState(false);
  const [retrying, setRetrying] = useState(false);
  const [retryError, setRetryError] = useState("");

  const retry = useCallback(async () => {
    setRetryError("");
    if (!data.retry) {
      onClose();
      return;
    }
    setRetrying(true);
    try {
      await data.retry();
      // Whatever the action changed, the screens reading it should see it.
      void qc.invalidateQueries();
      onClose();
    } catch (err) {
      setRetryError(err instanceof Error ? err.message.replace(/^\[[a-z_]+\]\s*/i, "") : "It failed again.");
    } finally {
      setRetrying(false);
    }
  }, [data, onClose, qc]);

  if (IDENTITY_REASONS.has(data.reason)) {
    return (
      <LinkIdentityModal
        intro={ACCOUNT_REQUIRED_MACHINE_COPY}
        message={data.message}
        onLinked={() => void retry()}
        onDismiss={onClose}
      />
    );
  }

  return (
    <Modal open={isOpen} onClose={onClose} title={paid ? "You're all set" : copy.title} size="lg">
      <div className="flex flex-col gap-4 px-6 py-4">
        {paid ? (
          <div className="space-y-4" data-testid="upgrade-paid">
            <div className="flex items-start gap-3 rounded-lg border border-emerald-500/30 bg-emerald-500/5 p-4">
              <CheckCircle2 className="mt-0.5 h-4 w-4 text-emerald-500" aria-hidden="true" />
              <p className="text-sm text-foreground">
                {data.reason === "reliant_credit_exhausted" ? "Credit added." : "Your plan is active."}{" "}
                {data.retry ? "Nothing was retried yet." : ""}
              </p>
            </div>
            {retryError && <p className="text-xs text-destructive-ink">{retryError}</p>}
            <Button className="w-full" onClick={() => void retry()} loading={retrying}>
              {data.retry ? (data.retryLabel ?? "Try again") : "Done"}
            </Button>
          </div>
        ) : (
          <>
            <div className="flex items-start gap-3">
              <div className="rounded-full bg-amber-100 p-2 text-amber-700 dark:bg-amber-900/40 dark:text-amber-300">
                <Zap className="h-5 w-5" />
              </div>
              <div className="flex-1 text-sm text-foreground">
                <p>{copy.body}</p>
                {data.message ? <p className="mt-2 text-xs text-muted-foreground">{data.message}</p> : null}
              </div>
            </div>
            {data.reason === "reliant_credit_exhausted" ? (
              <InlineCreditTopup onDone={() => setPaid(true)} />
            ) : COMPUTE_REASONS.has(data.reason) ? (
              <ComputeUpgrade onDone={() => setPaid(true)} />
            ) : null}
            <div className="flex justify-end gap-2">
              <Button variant="ghost" onClick={onClose}>
                Not now
              </Button>
              {!COMPUTE_REASONS.has(data.reason) && data.reason !== "reliant_credit_exhausted" && (
                <Button
                  onClick={() => {
                    onClose();
                    goToBilling();
                  }}
                >
                  Open billing
                </Button>
              )}
            </div>
          </>
        )}
      </div>
    </Modal>
  );
}

/** The compute checkout, mounted in the modal. */
function ComputeUpgrade({ onDone }: { onDone: () => void }) {
  const plansQ = usePlans();
  const subQ = useComputeSubscription();
  const currentPlanId = subQ.data?.subscription?.plan?.id ?? null;
  const options = useMemo(
    () =>
      deriveMachineOptions(
        sortPlansForDisplay((plansQ.data?.plans ?? []).filter(isPurchasableComputePlan)),
        plansQ.data?.daemonPricing,
      ),
    [plansQ.data],
  );
  // Start on the first plan the user is not already on.
  const [selected, setSelected] = useState<string | undefined>(undefined);
  const selectedPlanId = selected ?? options.find((o) => o.planId !== currentPlanId)?.planId;
  const [linkAttempt, setLinkAttempt] = useState(0);

  const landed = useCallback(async () => {
    if (!selectedPlanId) return false;
    try {
      const { data } = await subQ.refetch();
      return data?.subscription?.plan?.id === selectedPlanId;
    } catch {
      return false;
    }
  }, [subQ, selectedPlanId]);

  return (
    <ComputeSubscriptionCheckout
      key={`upgrade:${linkAttempt}`}
      plans={options}
      loadingPlans={plansQ.isLoading}
      selectedPlanId={selectedPlanId}
      onSelectPlan={(o) => setSelected(o.planId)}
      currentPlanId={currentPlanId}
      confirmSettlement={landed}
      onDone={onDone}
      renderIdentityRequired={(message) => (
        <LinkIdentityModal message={message} onLinked={() => setLinkAttempt((n) => n + 1)} onDismiss={() => undefined} />
      )}
    />
  );
}
