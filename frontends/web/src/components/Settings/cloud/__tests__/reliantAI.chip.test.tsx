import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";

/**
 * With billing's credit band now leading with a redeem action at equal
 * prominence to "Add credit", the AI page's second `RedeemCouponForm` is pure
 * duplication — and, worse, a second answer to "where do I put my code".
 *
 * So the balance stays here as a READ-ONLY chip (a user staring at an empty AI
 * balance should not have to guess why) and every way to ACT on it points at
 * the one purchase surface.
 */

const mocks = vi.hoisted(() => ({ available: true, goToBilling: vi.fn() }));

vi.mock("@/services/controlPlane/reliantAI", async (importOriginal) => {
  const actual = await importOriginal<Record<string, unknown>>();
  return {
    ...actual,
    get reliantAIAvailable() {
      return mocks.available;
    },
  };
});

vi.mock("@/hooks/useGoToBilling", () => ({
  useGoToBilling: () => mocks.goToBilling,
}));

const query = (data?: unknown) => ({
  data,
  isLoading: false,
  error: null,
  refetch: vi.fn(),
});
const mutation = () => ({
  mutate: vi.fn(),
  mutateAsync: vi.fn(),
  isPending: false,
  variables: undefined,
});

vi.mock("@/hooks/useReliantAIQueries", () => ({
  useReliantOverview: () => query({ allowedModels: [] }),
  useWalletOverview: () =>
    query({ wallet: { balanceUsdNanos: 1_234_560_000_000n } }),
  useLLMKeys: () => query([]),
  useAvailableModels: () => query([]),
  useLLMSpend: () => query({ entries: [], totalSpend: 0 }),
  useCreateLLMKey: () => mutation(),
  useRevokeLLMKey: () => mutation(),
  useRotateLLMKey: () => mutation(),
  useRedeemCoupon: () => mutation(),
}));

import { ReliantAISection } from "@/components/Settings/cloud/reliantAI";

function renderSection() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <ReliantAISection />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  mocks.available = true;
});

describe("one redemption surface, and it is billing's", () => {
  /**
   * The duplicate. Two coupon inputs on two pages means a user who redeems on
   * the wrong one still succeeds — the RPC is the same — but the two boxes
   * report different follow-on state, and only billing's shows both balances a
   * code can land in.
   */
  it("offers no coupon input of its own", () => {
    renderSection();

    expect(screen.queryByLabelText(/coupon code/i)).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /^redeem$/i }),
    ).not.toBeInTheDocument();
    expect(
      screen.queryByRole("button", { name: /have a coupon code/i }),
    ).not.toBeInTheDocument();
  });

  /**
   * Read-only does not mean dead-ended. Removing the form without leaving a
   * route to the real one would strand a user holding a code on the page they
   * naturally looked at first.
   */
  it("still shows the balance, and routes acting on it through the shared helper", async () => {
    const userEvent = (await import("@testing-library/user-event")).default;
    const { formatCurrencyFromWalletFields } = await import("../billingUtils");
    renderSection();

    expect(
      screen.getByText(formatCurrencyFromWalletFields(1_234_560_000_000n)),
    ).toBeInTheDocument();

    await userEvent
      .setup()
      .click(screen.getByRole("button", { name: /billing/i }));
    expect(mocks.goToBilling).toHaveBeenCalledTimes(1);
  });
});
