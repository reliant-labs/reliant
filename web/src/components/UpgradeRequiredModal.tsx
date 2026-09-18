import { ArrowUpRight, Zap } from "lucide-react";
import { Modal } from "./ui/Modal";
import type { UpgradeRequiredData } from "../store/modalStore";
import { useGoToBilling } from "@/hooks/useGoToBilling";

export interface UpgradeRequiredModalProps {
  isOpen: boolean;
  onClose: () => void;
  data: UpgradeRequiredData;
}

// Canonical reason codes, arriving either as the X-Reliant-Reason header on a
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
  // The one-time signup trial lapsed. Still emitted by svcdaemon.
  trial_expired: {
    title: "Your compute trial has ended",
    body: "Choose a compute plan, or redeem a coupon code, to keep running hosted machines.",
  },
  // The active plan does not permit the requested machine size.
  daemon_size_denied: {
    title: "That machine size isn't on your plan",
    body: "Your current plan doesn't include machines this large. Upgrade to unlock bigger machines, or pick a smaller size.",
  },
};

const GENERIC_COPY = {
  title: "Quota exceeded",
  body: "You've hit a plan limit. Upgrade to continue using this feature.",
};

/**
 * The CTA label per reason — "add credit" is not "upgrade".
 *
 * Every reason this modal can now receive is fixable by a billing action, so
 * the CTA is unconditional. There was briefly an opt-out for
 * `free_tier_global_budget`, a service-wide capacity cap no amount of the
 * user's money could clear; that reason no longer exists anywhere in
 * control-plane, and a set listing exceptions with no exceptions in it is
 * machinery pretending to make a decision.
 */
const CTA_LABEL: Record<string, string> = {
  reliant_credit_exhausted: "Add credit",
};

export function UpgradeRequiredModal({
  isOpen,
  onClose,
  data,
}: UpgradeRequiredModalProps) {
  const goToBilling = useGoToBilling();
  const copy = REASON_COPY[data.reason] ?? GENERIC_COPY;
  const ctaLabel = CTA_LABEL[data.reason] ?? "Upgrade plan";

  const handleUpgrade = () => {
    onClose();
    // The billing dashboard lives in-app at /settings/billing. goToBilling
    // routes an ANONYMOUS session through identity linking first — the users
    // who hit a credit wall are disproportionately the ones still on anonymous
    // sessions, and billing is a dead end without a real identity.
    goToBilling();
  };

  return (
    <Modal isOpen={isOpen} onClose={onClose} title={copy.title} size="sm">
      <div className="flex flex-col gap-4 p-6">
        <div className="flex items-start gap-3">
          <div className="rounded-full bg-amber-100 p-2 text-amber-700 dark:bg-amber-900/40 dark:text-amber-300">
            <Zap className="h-5 w-5" />
          </div>
          <div className="flex-1 text-sm text-foreground">
            <p>{copy.body}</p>
            {data.message ? (
              <p className="mt-2 text-xs text-muted-foreground">{data.message}</p>
            ) : null}
          </div>
        </div>

        <div className="flex justify-end gap-2">
          <button
            type="button"
            onClick={onClose}
            className="rounded-md border border-border px-4 py-2 text-sm text-foreground hover:bg-muted"
          >
            Not now
          </button>
          <button
            type="button"
            onClick={handleUpgrade}
            className="inline-flex items-center gap-1.5 rounded-md bg-primary px-4 py-2 text-sm font-medium text-primary-foreground hover:bg-primary/90"
          >
            {ctaLabel}
            <ArrowUpRight className="h-3.5 w-3.5" />
          </button>
        </div>
      </div>
    </Modal>
  );
}
