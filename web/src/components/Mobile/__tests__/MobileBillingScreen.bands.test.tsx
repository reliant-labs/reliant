import { render, screen, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";

/**
 * The phone gets the same READING of the two products as desktop, at a
 * narrower width.
 *
 * It is not a smaller dashboard — invoices, charts and the plan grid stay on
 * desktop deliberately — but "which of these two things am I looking at" must
 * not be a desktop-only affordance. Two cards drawn identically are exactly as
 * monotone on a phone, and worse, because there is less room for the copy that
 * would otherwise disambiguate them.
 *
 * Assertions target regions and contents, not classes: a class assertion pins
 * styling rather than behaviour and breaks on every visual tweak.
 */

const query = (data?: unknown) => ({
  data,
  isLoading: false,
  error: null,
  refetch: vi.fn(),
});
const mutation = () => ({ mutate: vi.fn(), mutateAsync: vi.fn(), isPending: false });

const state = vi.hoisted(() => ({
  sub: undefined as unknown,
  wallet: undefined as unknown,
  usage: undefined as unknown,
}));

vi.mock("../../../hooks/useCloudBillingQueries", () => ({
  useComputeSubscription: () => query(state.sub),
  useWalletOverview: () => query(state.wallet),
  useComputeUsage: () => query(state.usage),
  usePlans: () => query({ plans: [] }),
  useCreateCheckoutSession: () => mutation(),
  useCreateWalletTopupSession: () => mutation(),
  isCheckoutIdentityRequired: () => false,
}));

vi.mock("../../Billing/EmbeddedCheckoutPanel", () => ({
  EmbeddedCheckoutPanel: () => <div data-testid="embedded-checkout" />,
}));

const { MobileBillingScreen } = await import("../MobileBillingScreen");

function renderScreen() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MobileBillingScreen onBack={vi.fn()} />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  state.wallet = { overview: { wallet: { balanceUsdNanos: 18_400_000_000n } } };
  state.sub = {
    subscription: {
      plan: {
        id: "tier_beta",
        name: "Beta",
        productId: "prod_compute",
        priceCents: 4700n,
        structuredLimits: {
          allowedDaemonSizes: ["small", "medium"],
          daemonComputeIncludedMinutes: 2460,
          daemonOveragePerMinuteCents: 0.47,
        },
      },
    },
  };
  state.usage = {
    includedMinutes: 2460,
    usedMinutes: 720,
    overageMinutes: 0,
    estimatedOverageCostCents: 0,
    byDay: [],
    byWorkspace: [],
    grantedMinutesRemaining: 0,
    // Not optional. Omitting it makes the fixture describe a response no
    // measuring server sends, and the screen would correctly refuse to draw
    // hours for it — so a fixture without this flag tests the degraded path
    // while claiming to test the healthy one.
    usageMeasured: true,
  };
});

describe("the phone distinguishes the two products too", () => {
  it("gives each product its own labelled region", () => {
    renderScreen();
    const credit = screen.getByRole("region", { name: /ai credit/i });
    const compute = screen.getByRole("region", { name: /^compute$/i });
    expect(credit).not.toBe(compute);
  });

  /**
   * The owner's actual complaint, made falsifiable.
   *
   * "Two separate regions exist" is satisfied by two cards drawn identically —
   * which is exactly what the page did before, and exactly what he called
   * monotone. What has to hold is that the two products get DIFFERENT
   * treatments, and specifically that credit is the reservoir and compute is
   * the bordered contract rather than the reverse.
   *
   * This asserts the declared shape rather than a class string: a class
   * assertion pins styling, breaks on every visual tweak, and still would not
   * distinguish "different" from "swapped".
   */
  it("draws the two products in different containers, credit as the reservoir", () => {
    renderScreen();

    expect(
      screen.getByRole("region", { name: /ai credit/i }),
    ).toHaveAttribute("data-band-shape", "reservoir");
    expect(
      screen.getByRole("region", { name: /^compute$/i }),
    ).toHaveAttribute("data-band-shape", "contract");
  });

  /**
   * Same falsifiable split as desktop: each band carries its own metaphor's
   * readout and NOT the other's. Two cards that both showed a dollar figure
   * and a bar would satisfy "two regions exist" while being exactly as
   * monotone as what they replaced.
   */
  it("shows dollars in credit and hours-of-included in compute", () => {
    renderScreen();

    const credit = within(screen.getByRole("region", { name: /ai credit/i }));
    expect(credit.getByText("$18.40")).toBeInTheDocument();
    expect(credit.queryByText(/included/i)).toBeNull();

    const compute = within(screen.getByRole("region", { name: /^compute$/i }));
    expect(compute.getByText("Beta")).toBeInTheDocument();
    expect(compute.getByText(/12\.0 h/)).toBeInTheDocument();
    expect(compute.getByText(/41 h/)).toBeInTheDocument();
    expect(compute.queryByText("$18.40")).toBeNull();
  });

  /**
   * The degraded state travels. A phone showing `0 h` for a plan whose detail
   * failed to load is the same defect the owner screenshotted, and the smaller
   * screen makes a bare zero MORE alarming, not less.
   */
  it("names the stale-catalog cause rather than rendering a confident zero", () => {
    state.sub = {
      subscription: {
        plan: {
          id: "tier_beta",
          name: "Beta",
          productId: "prod_compute",
          priceCents: 4700n,
          structuredLimits: {
            allowedDaemonSizes: [],
            daemonComputeIncludedMinutes: 0,
            daemonOveragePerMinuteCents: 0,
          },
        },
      },
    };
    state.usage = undefined;
    renderScreen();

    const compute = within(screen.getByRole("region", { name: /^compute$/i }));
    expect(compute.getByText("Beta")).toBeInTheDocument();
    expect(compute.getByText(/plan details are unavailable/i)).toBeInTheDocument();
    expect(compute.queryByText(/0 h/)).toBeNull();
  });

  it("offers a purposeful empty state with no subscription", () => {
    state.sub = undefined;
    renderScreen();

    const compute = within(screen.getByRole("region", { name: /^compute$/i }));
    expect(compute.getByText(/no compute plan/i)).toBeInTheDocument();
    // Not the stale-catalog advice — a new user has nothing to restart.
    expect(compute.queryByText(/control plane/i)).toBeNull();
  });

  /**
   * The phone shares billingUtils with desktop for the NUMBERS, so it shared
   * the desktop defect too: a server that could not measure usage answered
   * 200 with used_minutes = 0, and this screen printed "0.0 h used" as fact.
   *
   * On a phone that is worse, not better — there is no surrounding detail to
   * contradict it, which is the same reasoning already written above the
   * `detailUnavailable` branch.
   *
   * Paired assertions, deliberately: suppressing the number unconditionally
   * would satisfy the first test alone while losing a genuine zero.
   */
  it("withholds hours used when the server did not measure", () => {
    state.usage = {
      includedMinutes: 2460,
      usedMinutes: 0,
      overageMinutes: 0,
      estimatedOverageCostCents: 0,
      byDay: [],
      byWorkspace: [],
      grantedMinutesRemaining: 0,
      usageMeasured: false,
    };
    renderScreen();

    const compute = within(screen.getByRole("region", { name: /^compute$/i }));
    expect(compute.queryByText(/0\.0 h used/i)).toBeNull();
    expect(compute.getByText(/usage unavailable/i)).toBeInTheDocument();
  });

  it("still prints 0.0 h used for a genuine zero", () => {
    state.usage = {
      includedMinutes: 2460,
      usedMinutes: 0,
      overageMinutes: 0,
      estimatedOverageCostCents: 0,
      byDay: [],
      byWorkspace: [],
      grantedMinutesRemaining: 0,
      usageMeasured: true,
    };
    renderScreen();

    const compute = within(screen.getByRole("region", { name: /^compute$/i }));
    expect(compute.getByText(/0\.0 h used/i)).toBeInTheDocument();
    expect(compute.queryByText(/usage unavailable/i)).toBeNull();
  });

  it("names no payment brand", () => {
    renderScreen();
    const text = document.body.textContent ?? "";
    for (const brand of ["PayPal", "Apple Pay", "Google Pay", "Visa", "Mastercard"]) {
      expect(text).not.toContain(brand);
    }
  });
});
