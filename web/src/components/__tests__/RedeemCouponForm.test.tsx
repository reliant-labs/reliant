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

vi.mock("../Billing/LinkIdentityModal", () => ({
  LinkIdentityModal: ({ onLinked, onDismiss }: { onLinked: () => void; onDismiss: () => void }) => (
    <div data-testid="link-identity-modal">
      <button onClick={onLinked}>linked</button>
      <button onClick={onDismiss}>dismiss</button>
    </div>
  ),
}));

import { RedeemCouponForm } from "../RedeemCouponForm";
import { ConnectError, Code } from "@connectrpc/connect";
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

describe("RedeemCouponForm — account_required", () => {
  const refusal = () =>
    new ConnectError(
      "sign in with an email account to redeem a compute coupon",
      Code.FailedPrecondition,
      new Headers({ "x-reliant-reason": "account_required" }),
    );

  function failWith(err: unknown, call = 0) {
    const opts = mutate.mock.calls[call][1] as { onError?: (e: unknown) => void };
    act(() => opts.onError?.(err));
  }

  it("asks for an identity, then re-submits the same code exactly once", async () => {
    render(<RedeemCouponForm variant="open" />);
    await userEvent.type(screen.getByLabelText(/coupon code/i), "CLOUDCODE20");
    await userEvent.click(screen.getByRole("button", { name: /redeem/i }));
    expect(mutate).toHaveBeenCalledTimes(1);

    failWith(refusal());
    expect(await screen.findByTestId("link-identity-modal")).toBeInTheDocument();
    // The refusal is a prompt, not an error line.
    expect(screen.queryByText(/sign in with an email/i)).toBeNull();
    expect(mutate).toHaveBeenCalledTimes(1);

    await userEvent.click(screen.getByText("linked"));

    expect(mutate).toHaveBeenCalledTimes(2);
    expect(mutate.mock.calls[1][0]).toBe("CLOUDCODE20");
    expect(screen.queryByTestId("link-identity-modal")).toBeNull();
  });

  it("does not retry when the user dismisses the modal", async () => {
    render(<RedeemCouponForm variant="open" />);
    await userEvent.type(screen.getByLabelText(/coupon code/i), "CLOUDCODE20");
    await userEvent.click(screen.getByRole("button", { name: /redeem/i }));
    failWith(refusal());
    await userEvent.click(await screen.findByText("dismiss"));

    expect(mutate).toHaveBeenCalledTimes(1);
  });

  it("still shows other refusals inline", async () => {
    render(<RedeemCouponForm variant="open" />);
    await userEvent.type(screen.getByLabelText(/coupon code/i), "NOPE");
    await userEvent.click(screen.getByRole("button", { name: /redeem/i }));
    failWith(new ConnectError("coupon already redeemed", Code.AlreadyExists));

    expect(await screen.findByText(/already redeemed/i)).toBeInTheDocument();
    expect(screen.queryByTestId("link-identity-modal")).toBeNull();
  });
});
