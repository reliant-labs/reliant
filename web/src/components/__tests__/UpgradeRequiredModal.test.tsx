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
 * These tests pin the three properties that keep that from coming back:
 *
 *   1. No reason's copy claims a free tier exists.
 *   2. An exhausted wallet is described as a credit balance, with an "Add
 *      credit" action.
 *   3. The service-wide operator cap — which money CANNOT clear — offers no
 *      billing action at all, because taking payment for something that stays
 *      blocked is worse than showing no button.
 */
import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";

const goToBilling = vi.fn();
vi.mock("@/hooks/useGoToBilling", () => ({
  useGoToBilling: () => goToBilling,
}));

// The Modal primitive portals and manages focus; neither matters here, and
// rendering children inline keeps the assertions about copy rather than chrome.
vi.mock("../ui/Modal", () => ({
  Modal: ({
    title,
    children,
  }: {
    title?: string;
    children: React.ReactNode;
  }) => (
    <div>
      <h2>{title}</h2>
      {children}
    </div>
  ),
}));

import { UpgradeRequiredModal } from "../UpgradeRequiredModal";

function renderModal(reason: string, message = "") {
  return render(
    <UpgradeRequiredModal
      isOpen
      onClose={() => {}}
      data={{ reason, message, upgradeUrl: "" }}
    />,
  );
}

// Every reason the app can actually deliver, plus an unrecognized one to cover
// the generic fallback.
//
// Three codes used to be here and are now gone from control-plane entirely:
// `free_tier_global_budget` (a service-wide operator cap),
// `free_tier_compute_minutes` (a per-owner free allowance), and `trial_expired`
// (a signup compute trial that no longer exists — there is no free compute).
// Nothing can emit any of them, so the modal no longer carries copy for them.
const ALL_REASONS = [
  "reliant_credit_exhausted",
  "compute_budget_exhausted",
  "no_compute_subscription",
  "daemon_size_denied",
  "something_we_have_never_seen",
];

describe("UpgradeRequiredModal", () => {
  it("never tells the user there is a free tier", () => {
    for (const reason of ALL_REASONS) {
      const { unmount } = renderModal(reason);
      // Matches "free tier", "free-tier", and "free compute minutes".
      expect(document.body.textContent).not.toMatch(/free[\s-]?(tier|compute)/i);
      unmount();
    }
  });

  it("describes an empty wallet as credit, and offers to add credit", () => {
    renderModal("reliant_credit_exhausted");

    expect(screen.getByText(/out of Reliant credit/i)).toBeTruthy();
    expect(screen.getByRole("button", { name: /add credit/i })).toBeTruthy();
    // "Upgrade" would be the wrong verb: credit is a top-up, not a tier.
    expect(screen.queryByRole("button", { name: /upgrade/i })).toBeNull();
  });

  it("keeps an upgrade action for compute reasons and for unknown reasons", () => {
    const { unmount } = renderModal("compute_budget_exhausted");
    expect(screen.getByRole("button", { name: /upgrade plan/i })).toBeTruthy();
    unmount();

    // An unrecognized reason must not silently lose its action — the generic
    // copy tells the user to upgrade, so the button has to be there to obey it.
    renderModal("something_we_have_never_seen");
    expect(screen.getByRole("button", { name: /upgrade plan/i })).toBeTruthy();
  });

  it("renders the server's own message alongside our copy", () => {
    // The backend message is the authoritative account of what happened, so it
    // is shown verbatim. Our per-reason copy must not contradict it.
    renderModal("reliant_credit_exhausted", "wallet balance is 0");

    expect(screen.getByText("wallet balance is 0")).toBeTruthy();
  });
});
