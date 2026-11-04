/**
 * UpgradeRequiredModal copy and call-to-action, per reason code.
 *
 * ── Why this file exists ──
 *
 * A user who had never entered a coupon or a card, and who had only *selected*
 * the Reliant provider during onboarding, was shown: "The shared free-tier
 * budget for the month is exhausted. Upgrade to a paid plan to continue."
 *
 * Two things were wrong with that. The pool it describes was deleted with
 * migration 00062_drop_free_tier_llm_pool, and we no longer offer a free tier
 * at all — so the sentence described a mechanism that does not exist. And the
 * condition that actually tripped was the user's own empty wallet, which is a
 * top-up, not a tier.
 *
 * These tests pin what keeps that from coming back, plus how the modal fixes
 * the refusal in place:
 *
 *   1. No reason's copy claims a free tier exists.
 *   2. An exhausted wallet is described as a credit balance, and the credit
 *      top-up mounts in the modal — no trip to billing.
 *   3. A compute reason mounts the compute checkout in the modal; after it
 *      settles, ONE explicit button repeats the refused request. Nothing
 *      billable re-runs on its own.
 *   4. A missing email (account_required, billing_email_missing) is the one
 *      account prompt, and the refused request re-runs once it is linked.
 */
import { describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";

const goToBilling = vi.fn();
vi.mock("@/hooks/useGoToBilling", () => ({
  useGoToBilling: () => goToBilling,
}));

vi.mock("../forge-ui/modal", () => ({
  default: ({ title, children }: { title?: string; children: React.ReactNode }) => (
    <div>
      <h2>{title}</h2>
      {children}
    </div>
  ),
}));

vi.mock("../Billing/LinkIdentityModal", () => ({
  LinkIdentityModal: ({ intro, onLinked }: { intro?: string; onLinked: () => void }) => (
    <div data-testid="link-identity-modal">
      {intro}
      <button type="button" onClick={onLinked}>
        Link account
      </button>
    </div>
  ),
}));
vi.mock("../Billing/ComputeSubscriptionCheckout", () => ({
  ComputeSubscriptionCheckout: ({ onDone }: { onDone: () => void }) => (
    <button type="button" onClick={onDone}>
      Pay for a plan
    </button>
  ),
}));
vi.mock("../Billing/InlineCreditTopup", () => ({
  InlineCreditTopup: ({ onDone }: { onDone: () => void }) => (
    <button type="button" onClick={onDone}>
      Add credit
    </button>
  ),
}));
vi.mock("@/hooks/useCloudBillingQueries", () => ({
  usePlans: () => ({ isLoading: false, data: { plans: [] } }),
  useComputeSubscription: () => ({ data: {}, refetch: vi.fn() }),
}));
vi.mock("@tanstack/react-query", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@tanstack/react-query")>()),
  useQueryClient: () => ({ invalidateQueries: vi.fn() }),
}));

import { UpgradeRequiredModal } from "../UpgradeRequiredModal";
import type { UpgradeRequiredData } from "../../store/modalStore";

function renderModal(reason: string, message = "", extra: Partial<UpgradeRequiredData> = {}, onClose = () => {}) {
  return render(<UpgradeRequiredModal isOpen onClose={onClose} data={{ reason, message, ...extra }} />);
}

const ALL_REASONS = [
  "reliant_credit_exhausted",
  "compute_budget_exhausted",
  "no_compute_subscription",
  "daemon_size_denied",
  "daemon_storage_denied",
  "something_we_have_never_seen",
];

describe("UpgradeRequiredModal", () => {
  it("never tells the user there is a free tier", () => {
    for (const reason of ALL_REASONS) {
      const { unmount } = renderModal(reason);
      expect(document.body.textContent).not.toMatch(/free[\s-]?(tier|compute)/i);
      unmount();
    }
  });

  it("describes an empty wallet as credit, and tops it up in place", () => {
    renderModal("reliant_credit_exhausted");
    expect(screen.getByText(/out of Reliant credit/i)).toBeTruthy();
    expect(screen.getByRole("button", { name: /add credit/i })).toBeTruthy();
    expect(screen.queryByRole("button", { name: /upgrade/i })).toBeNull();
    expect(goToBilling).not.toHaveBeenCalled();
  });

  it("takes payment in place for a compute reason, then repeats the action only on a click", async () => {
    const retry = vi.fn().mockResolvedValue(undefined);
    const onClose = vi.fn();
    renderModal("no_compute_subscription", "", { retry, retryLabel: "Start my machine" }, onClose);

    fireEvent.click(screen.getByRole("button", { name: "Pay for a plan" }));
    expect(await screen.findByTestId("upgrade-paid")).toBeTruthy();
    expect(retry).not.toHaveBeenCalled();

    fireEvent.click(screen.getByRole("button", { name: "Start my machine" }));
    await waitFor(() => expect(retry).toHaveBeenCalledTimes(1));
    await waitFor(() => expect(onClose).toHaveBeenCalled());
    expect(goToBilling).not.toHaveBeenCalled();
  });

  it("keeps a way to billing for a reason it cannot fix in place", () => {
    renderModal("something_we_have_never_seen");
    expect(screen.getByRole("button", { name: /open billing/i })).toBeTruthy();
  });

  it("renders the server's own message alongside our copy", () => {
    renderModal("reliant_credit_exhausted", "wallet balance is 0");
    expect(screen.getByText("wallet balance is 0")).toBeTruthy();
  });

  it("is the one account prompt for account_required and billing_email_missing, and re-runs the call once linked", async () => {
    for (const reason of ["account_required", "billing_email_missing"]) {
      const retry = vi.fn().mockResolvedValue(undefined);
      const { unmount } = renderModal(reason, "needs an email", { retry });
      expect(screen.getByTestId("link-identity-modal").textContent).toMatch(/add an email/i);
      fireEvent.click(screen.getByRole("button", { name: "Link account" }));
      await waitFor(() => expect(retry).toHaveBeenCalledTimes(1));
      unmount();
    }
    expect(goToBilling).not.toHaveBeenCalled();
  });
});
