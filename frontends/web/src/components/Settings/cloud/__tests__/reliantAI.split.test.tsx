/**
 * The split: `/settings/billing` is where money is spent, this page is where
 * AI access is configured.
 *
 * The two pages were not two halves of one thing — billing is wallet, plan,
 * invoices, usage; this is LLM keys, allowed models, spend caps. What WAS
 * duplicated ran in one direction only: the credit balance appeared on both,
 * formatted by two different functions, and this page offered its own doors
 * into billing that bypassed the shared navigation helper and so arrived
 * without the tab, the origin, or the route back.
 *
 * So the balance stays here as a READ-ONLY chip — a user staring at an empty
 * AI balance should not have to guess why — and every way to act on it points
 * at the one purchase surface.
 */

import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";

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

/**
 * The shared navigation helper. Spying on it is the point: this page had two
 * bare `navigate` calls that reached billing WITHOUT `tab=plans`, `from`, or
 * `returnTo`, so a user sent from here landed on a dashboard with no memory of
 * why they came. Asserting "navigation happened" would not have caught that —
 * asserting it went through the helper does.
 */
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

const walletState = vi.hoisted(() => ({ current: undefined as unknown }));

vi.mock("@/hooks/useReliantAIQueries", () => ({
  useReliantOverview: () => query({ allowedModels: [] }),
  useWalletOverview: () => query(walletState.current),
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
  // A funded wallet, so the page renders its normal body rather than the
  // no-credit explainer.
  // Over $1,000 ON PURPOSE. The two formatters agree on small values —
  // "$18.40" either way — so a smaller fixture cannot tell them apart and the
  // test would pass against the duplicate it exists to remove. Intl (the local
  // helper) writes "$1,234.56"; formatCurrencyFromNanos writes "$1234.56".
  walletState.current = { wallet: { balanceUsdNanos: 1_234_560_000_000n } };
});

describe("the balance is shown here but not topped up here", () => {
  /**
   * One wallet, one number, one formatter. This page had its own
   * `usdFromNanos` while billing used `formatCurrencyFromWalletFields` — two
   * independent renderings of a single balance, free to disagree on rounding.
   */
  it("formats the balance the same way billing does", async () => {
    const { formatCurrencyFromWalletFields } = await import("../billingUtils");
    renderSection();

    const shared = formatCurrencyFromWalletFields(1_234_560_000_000n);
    expect(shared).toBe("$1234.56"); // pins WHICH rendering, not just "some"
    expect(screen.getByText(shared)).toBeInTheDocument();
    // The local Intl-based helper this replaced grouped thousands.
    expect(screen.queryByText("$1,234.56")).not.toBeInTheDocument();
  });

  /**
   * The purchase affordances belong on the one page that spends money. A
   * second set of top-up buttons here is a competing door, and it is what
   * made "where do I pay?" ambiguous.
   */
  it("offers no top-up amounts of its own", () => {
    renderSection();

    for (const amount of ["$10", "$25", "$50", "$100"]) {
      expect(
        screen.queryByRole("button", { name: new RegExp(`^\\${amount}$`) }),
      ).not.toBeInTheDocument();
    }
  });
});

describe("every route to billing goes through the shared helper", () => {
  it("sends a user with no credit to billing via useGoToBilling", async () => {
    walletState.current = { wallet: { balanceUsdNanos: 0n } };
    renderSection();
    const user = userEvent.setup();

    await user.click(screen.getByRole("button", { name: /add credit/i }));

    expect(mocks.goToBilling).toHaveBeenCalledTimes(1);
  });

  it("sends a user from the coupon footnote to billing via the same helper", async () => {
    renderSection();
    const user = userEvent.setup();

    await user.click(screen.getByRole("button", { name: /^billing$/i }));

    expect(mocks.goToBilling).toHaveBeenCalledTimes(1);
  });
});
