/**
 * A coupon redemption credits control-plane's wallet but does not, by
 * itself, put an `rlnt_` key on this device — that requires a separate
 * SyncReliantProvider call (see `onboardingService.provisionManagedKey`).
 * Without it, a user who redeems an AI-credit coupon sees the wallet
 * credited but the onboarding checklist's "Add an API key" item stays
 * unchecked forever, because it only completes via `api-key:saved` or by
 * polling `getProviders()` for a key that was never synced.
 *
 * These tests pin: WALLET_CREDIT (AI credit) redemptions must sync the
 * provider and emit `api-key:saved`; COMPUTE_MINUTES (machine time)
 * redemptions must NOT — that coupon buys no AI access, so completing the
 * checklist for it would be a new bug of the same shape. A sync failure
 * must not be swallowed silently, but must also not undo the (successful)
 * redemption.
 */
import { act, render, screen, waitFor } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { beforeEach, describe, expect, it, vi } from "vitest";

const mutate = vi.fn();
vi.mock("@/hooks/useReliantAIQueries", () => ({
  useRedeemCoupon: () => ({ mutate, isPending: false }),
}));

const provisionManagedKey = vi.fn();
vi.mock("@/services/controlPlane/onboarding", () => ({
  onboardingService: {
    provisionManagedKey: (...args: unknown[]) => provisionManagedKey(...args),
  },
}));

const emit = vi.fn();
vi.mock("@/lib/events", () => ({
  getEventBus: () => ({ emit }),
}));

vi.mock("@/lib/logger", () => ({
  logger: { warn: vi.fn(), info: vi.fn(), error: vi.fn(), debug: vi.fn() },
}));

import { RedeemCouponForm } from "../RedeemCouponForm";
import { RedeemedCouponKind } from "@/services/controlPlane/reliantAI";

function triggerSuccess(result: Record<string, unknown>) {
  const call = mutate.mock.calls[0];
  const opts = call[1] as { onSuccess?: (r: unknown) => void };
  act(() => {
    opts.onSuccess?.(result);
  });
}

beforeEach(() => {
  vi.clearAllMocks();
  provisionManagedKey.mockResolvedValue({ synced: true });
});

describe("RedeemCouponForm — reliant provider sync", () => {
  it("syncs the reliant provider and marks the checklist item after an AI-credit redemption", async () => {
    render(<RedeemCouponForm variant="open" />);
    await userEvent.type(screen.getByLabelText(/coupon code/i), "FREE20");
    await userEvent.click(screen.getByRole("button", { name: /redeem/i }));

    triggerSuccess({
      kind: RedeemedCouponKind.WALLET_CREDIT,
      amountCents: 2000,
      newBalanceCents: 2000,
      code: "FREE20",
      computeMinutes: 0,
      newComputeMinutesRemaining: 0,
    });

    await waitFor(() => {
      expect(provisionManagedKey).toHaveBeenCalled();
    });
    await waitFor(() => {
      expect(emit).toHaveBeenCalledWith("api-key:saved", { provider: "reliant" });
    });
  });

  it("does NOT sync the reliant provider after a compute-minutes redemption", async () => {
    render(<RedeemCouponForm variant="open" />);
    await userEvent.type(screen.getByLabelText(/coupon code/i), "MACHINE60");
    await userEvent.click(screen.getByRole("button", { name: /redeem/i }));

    triggerSuccess({
      kind: RedeemedCouponKind.COMPUTE_MINUTES,
      amountCents: 0,
      newBalanceCents: 0,
      code: "MACHINE60",
      computeMinutes: 600,
      newComputeMinutesRemaining: 600,
    });

    // Give any stray async work a tick to (not) run.
    await new Promise((r) => setTimeout(r, 0));
    expect(provisionManagedKey).not.toHaveBeenCalled();
    expect(emit).not.toHaveBeenCalledWith("api-key:saved", expect.anything());
  });

  it("surfaces a warning, without undoing the redemption, when sync fails", async () => {
    provisionManagedKey.mockRejectedValue(new Error("network down"));

    render(<RedeemCouponForm variant="open" />);
    await userEvent.type(screen.getByLabelText(/coupon code/i), "FREE20");
    await userEvent.click(screen.getByRole("button", { name: /redeem/i }));

    triggerSuccess({
      kind: RedeemedCouponKind.WALLET_CREDIT,
      amountCents: 2000,
      newBalanceCents: 2000,
      code: "FREE20",
      computeMinutes: 0,
      newComputeMinutesRemaining: 0,
    });

    // The success message (redemption itself) must still be shown.
    await waitFor(() => {
      expect(screen.getByText(/added \$20\.00 to your balance/i)).toBeInTheDocument();
    });
    // But something actionable about the sync failure must appear too.
    await waitFor(() => {
      expect(
        screen.getByText(/sync|manually|try again|settings/i),
      ).toBeInTheDocument();
    });
    expect(emit).not.toHaveBeenCalledWith("api-key:saved", expect.anything());
  });
});
