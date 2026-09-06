/**
 * The purchase surface: pick a machine size, pick the plan that runs it, pay
 * without leaving the page.
 *
 * Three properties are pinned here, and each replaces something that used to
 * be true and was wrong:
 *
 * 1. **Buying never navigates.** The plan grid called `openCheckout`, which
 *    set `window.location.href` to a hosted Stripe URL. On desktop that
 *    escaped to the system browser; on mobile it backgrounded the tab. The
 *    assertion is not "a panel appeared" but "a panel appeared AND the
 *    location did not change AND nothing built a hosted return URL", because
 *    a surface can mount the panel and still redirect.
 *
 * 2. **A size outside the plan's allowed set cannot be chosen.** Plan and size
 *    are one axis (`PlanLimits.allowed_daemon_sizes`); a picker that lets them
 *    disagree produces a purchase the server refuses at CreateDaemon time.
 *
 * 3. **Prices come off the wire.** The fixture is deliberately REPRICED — the
 *    catalog's real tiers are $20/$40/$80 and these are not — so a hardcoded
 *    fallback keyed on plan id cannot coincidentally satisfy the assertions.
 */

import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";

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
});

const routerState = vi.hoisted(() => ({
  search: {} as Record<string, unknown>,
  navigate: vi.fn(),
}));
const subState = vi.hoisted(() => ({ current: undefined as unknown }));
const checkoutCalls = vi.hoisted(() => ({ mutate: vi.fn() }));
const stripeNav = vi.hoisted(() => ({ open: vi.fn(), buildUrls: vi.fn() }));

vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => routerState.navigate,
  useSearch: () => routerState.search,
}));

/**
 * `lib/stripeCheckout` is the hosted round trip. Spying on it — rather than
 * only asserting that a panel rendered — is what makes "never navigates"
 * falsifiable: a page could mount the embedded panel and ALSO redirect, and
 * an assertion about rendered output alone would not notice.
 */
vi.mock("@/lib/stripeCheckout", async (importOriginal) => {
  const actual = await importOriginal<Record<string, unknown>>();
  return {
    ...actual,
    openCheckout: stripeNav.open,
    buildCheckoutReturnUrls: (...args: unknown[]) => {
      stripeNav.buildUrls(...args);
      return { successUrl: "https://example.test/s", cancelUrl: "https://example.test/c" };
    },
  };
});

/**
 * The catalog, REPRICED away from the real tiers, with ids the client has
 * never heard of. $23 / $47 / $91 match no constant that ever existed here.
 */
const PLANS = [
  {
    id: "tier_alpha",
    name: "Alpha",
    productId: "prod_compute",
    priceCents: 2300n,
    displayOrder: 1,
    structuredLimits: {
      allowedDaemonSizes: ["small"],
      daemonComputeIncludedMinutes: 1200,
      daemonOveragePerMinuteCents: 0.25,
    },
  },
  {
    id: "tier_beta",
    name: "Beta",
    productId: "prod_compute",
    priceCents: 4700n,
    displayOrder: 2,
    structuredLimits: {
      allowedDaemonSizes: ["small", "medium"],
      daemonComputeIncludedMinutes: 2600,
      daemonOveragePerMinuteCents: 0.47,
    },
  },
  {
    id: "tier_gamma",
    name: "Gamma",
    productId: "prod_compute",
    priceCents: 9100n,
    displayOrder: 3,
    structuredLimits: {
      allowedDaemonSizes: ["small", "medium", "large"],
      daemonComputeIncludedMinutes: 5200,
      daemonOveragePerMinuteCents: 0.91,
    },
  },
];

vi.mock("@/hooks/useCloudBillingQueries", async (importOriginal) => {
  const actual = await importOriginal<Record<string, unknown>>();
  return {
    ...actual,
    cloudBillingKeys: {
      all: ["cloud-billing"],
      computeSubscription: ["cloud-billing", "compute-subscription"],
      walletOverview: ["cloud-billing", "wallet-overview"],
      computeUsage: (p: string) => ["cloud-billing", "compute-usage", p],
      plans: ["cloud-billing", "plans"],
      invoices: ["cloud-billing", "invoices"],
      billingEmail: ["cloud-billing", "billing-email"],
    },
    useComputeSubscription: () => query(subState.current),
    useWalletOverview: () => query(undefined),
    useComputeUsage: () => query(undefined),
    usePlans: () => query({ plans: PLANS }),
    useCurrentUserInvoices: () => query(undefined),
    useBillingEmail: () => query(undefined),
    useSetComputeOverage: () => mutation(),
    useCreateCheckoutSession: () => ({
      ...mutation(),
      mutate: checkoutCalls.mutate,
    }),
    useCreateWalletTopupSession: () => mutation(),
    useCreateBillingPortalSession: () => mutation(),
    useUpdateBillingEmail: () => mutation(),
  };
});

// Stubbed so this file tests the SURFACE — that the right purchase mounts in
// place. The panel's own behaviour is covered in components/Billing.
vi.mock("@/components/Billing/EmbeddedCheckoutPanel", () => ({
  EmbeddedCheckoutPanel: ({
    request,
  }: {
    request: { kind: string; planId?: string; amountCents?: bigint };
  }) => (
    <div
      data-testid="embedded-checkout"
      data-kind={request.kind}
      data-plan-id={request.planId ?? ""}
      data-amount={request.amountCents?.toString() ?? ""}
    >
      embedded checkout
    </div>
  ),
}));

import { BillingSection } from "@/components/Settings/cloud/billing";

function renderSection() {
  const client = new QueryClient({
    defaultOptions: { queries: { retry: false } },
  });
  return render(
    <QueryClientProvider client={client}>
      <BillingSection />
    </QueryClientProvider>,
  );
}

/** Open the Plans tab, which is the purchase surface. */
async function openPlans(user: ReturnType<typeof userEvent.setup>) {
  routerState.search = { tab: "plans" };
  renderSection();
  return user;
}

beforeEach(() => {
  vi.clearAllMocks();
  routerState.search = {};
  subState.current = undefined;
});

describe("plans render the server's prices", () => {
  it("shows each plan's price from the wire, not a client constant", async () => {
    const user = userEvent.setup();
    await openPlans(user);

    // $23 / $47 / $91 exist in no table the client ever had.
    expect(screen.getByText("$23")).toBeInTheDocument();
    expect(screen.getByText("$47")).toBeInTheDocument();
    expect(screen.getByText("$91")).toBeInTheDocument();
  });

  it("shows the server's included hours and overage rate", async () => {
    const user = userEvent.setup();
    await openPlans(user);

    // 2600 min = 43 h, at $0.0047/min. Neither is a round number any
    // hardcoded fallback used.
    expect(screen.getByText(/43 hours included/i)).toBeInTheDocument();
    expect(screen.getByText(/\$0\.005\/min/i)).toBeInTheDocument();
  });
});

describe("size selection is bounded by the plan", () => {
  it("offers the sizes the catalog actually sells", async () => {
    const user = userEvent.setup();
    await openPlans(user);

    const sizes = screen.getByRole("radiogroup", { name: /machine size/i });
    expect(within(sizes).getByRole("radio", { name: /small/i })).toBeInTheDocument();
    expect(within(sizes).getByRole("radio", { name: /medium/i })).toBeInTheDocument();
    expect(within(sizes).getByRole("radio", { name: /large/i })).toBeInTheDocument();
    // No plan in this catalog runs XL, so it must not be offered at all.
    expect(within(sizes).queryByRole("radio", { name: /^xl$/i })).not.toBeInTheDocument();
  });

  /**
   * The core constraint. Choosing Large must make Alpha and Beta — which do
   * not allow it — unselectable, not merely styled differently. `disabled` is
   * asserted on the control the user would click, because a card that looks
   * dimmed but still fires a purchase is the bug, not the fix.
   */
  it("cannot buy a plan that does not allow the chosen size", async () => {
    const user = userEvent.setup();
    await openPlans(user);

    await user.click(
      within(screen.getByRole("radiogroup", { name: /machine size/i })).getByRole(
        "radio",
        { name: /large/i },
      ),
    );

    const alpha = screen.getByTestId("plan-card-tier_alpha");
    const gamma = screen.getByTestId("plan-card-tier_gamma");

    expect(within(alpha).getByRole("button", { name: /alpha/i })).toBeDisabled();
    expect(
      within(screen.getByTestId("plan-card-tier_beta")).getByRole("button", {
        name: /beta/i,
      }),
    ).toBeDisabled();
    expect(within(gamma).getByRole("button", { name: /gamma/i })).toBeEnabled();
  });

  it("says why a plan is unavailable rather than just dimming it", async () => {
    const user = userEvent.setup();
    await openPlans(user);

    await user.click(
      within(screen.getByRole("radiogroup", { name: /machine size/i })).getByRole(
        "radio",
        { name: /large/i },
      ),
    );

    expect(
      within(screen.getByTestId("plan-card-tier_alpha")).getByText(
        /doesn't run large machines/i,
      ),
    ).toBeInTheDocument();
  });

  it("re-enables a plan when the size is narrowed back", async () => {
    const user = userEvent.setup();
    await openPlans(user);
    const sizes = screen.getByRole("radiogroup", { name: /machine size/i });

    await user.click(within(sizes).getByRole("radio", { name: /large/i }));
    expect(
      within(screen.getByTestId("plan-card-tier_alpha")).getByRole("button", {
        name: /alpha/i,
      }),
    ).toBeDisabled();

    await user.click(within(sizes).getByRole("radio", { name: /small/i }));
    expect(
      within(screen.getByTestId("plan-card-tier_alpha")).getByRole("button", {
        name: /alpha/i,
      }),
    ).toBeEnabled();
  });

  /**
   * A disabled button is a claim about the DOM; this is a claim about money.
   * Clicking the refused plan must mount no checkout at all.
   */
  it("mounts no checkout when a refused plan is clicked", async () => {
    const user = userEvent.setup();
    await openPlans(user);

    await user.click(
      within(screen.getByRole("radiogroup", { name: /machine size/i })).getByRole(
        "radio",
        { name: /large/i },
      ),
    );
    await user.click(
      within(screen.getByTestId("plan-card-tier_alpha")).getByRole("button", {
        name: /alpha/i,
      }),
    );

    expect(screen.queryByTestId("embedded-checkout")).not.toBeInTheDocument();
    expect(checkoutCalls.mutate).not.toHaveBeenCalled();
  });
});

describe("purchasing happens in place", () => {
  it("mounts the embedded panel for the chosen plan", async () => {
    const user = userEvent.setup();
    await openPlans(user);
    expect(screen.queryByTestId("embedded-checkout")).not.toBeInTheDocument();

    await user.click(
      within(screen.getByTestId("plan-card-tier_beta")).getByRole("button", {
        name: /beta/i,
      }),
    );

    const panel = screen.getByTestId("embedded-checkout");
    expect(panel).toHaveAttribute("data-kind", "compute_plan");
    expect(panel).toHaveAttribute("data-plan-id", "tier_beta");
  });

  /**
   * The behaviour the redesign exists to remove. Asserted three ways because
   * any one of them alone can pass against a page that still redirects: the
   * location is unchanged, the hosted opener was never called, and no hosted
   * return URL was even built.
   */
  it("never navigates to a hosted Stripe URL", async () => {
    const user = userEvent.setup();
    const before = window.location.href;
    await openPlans(user);

    await user.click(
      within(screen.getByTestId("plan-card-tier_beta")).getByRole("button", {
        name: /beta/i,
      }),
    );

    expect(window.location.href).toBe(before);
    expect(stripeNav.open).not.toHaveBeenCalled();
    expect(stripeNav.buildUrls).not.toHaveBeenCalled();
    expect(routerState.navigate).not.toHaveBeenCalled();
  });

  it("lets the user back out of a purchase without buying", async () => {
    const user = userEvent.setup();
    await openPlans(user);

    await user.click(
      within(screen.getByTestId("plan-card-tier_beta")).getByRole("button", {
        name: /beta/i,
      }),
    );
    expect(screen.getByTestId("embedded-checkout")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: /cancel/i }));
    expect(screen.queryByTestId("embedded-checkout")).not.toBeInTheDocument();
  });
});

describe("adding AI credit", () => {
  /**
   * The other half of "one place to spend money": credit top-ups buy through
   * the same panel, so neither purchase leaves the page.
   */
  it("mounts the embedded panel for a top-up amount", async () => {
    renderSection();
    const user = userEvent.setup();

    await user.click(screen.getByRole("button", { name: /^\$25$/ }));

    const panel = screen.getByTestId("embedded-checkout");
    expect(panel).toHaveAttribute("data-kind", "wallet_topup");
    expect(panel).toHaveAttribute("data-amount", "2500");
  });

  it("does not open a hosted top-up URL", async () => {
    const before = window.location.href;
    renderSection();
    const user = userEvent.setup();

    await user.click(screen.getByRole("button", { name: /^\$25$/ }));

    expect(window.location.href).toBe(before);
    expect(stripeNav.open).not.toHaveBeenCalled();
  });
});

describe("the onboarding detour still has a way home", () => {
  /**
   * `billing.tsx` reads `from` / `returnTo` to offer "Back to setup". The
   * embedded panel removed the Stripe round trip that used to carry those,
   * but a user can still ARRIVE here mid-wizard, and the route back must
   * survive that.
   */
  it("offers 'Back to setup' to a user who came from onboarding", () => {
    routerState.search = { tab: "plans", from: "onboarding", returnTo: "/onboarding?plan=x" };
    renderSection();

    expect(
      screen.getByRole("button", { name: /back to setup/i }),
    ).toBeInTheDocument();
  });

  it("navigates to the captured returnTo, not to a bare /onboarding", async () => {
    routerState.search = {
      tab: "plans",
      from: "onboarding",
      returnTo: "/onboarding?plan=eyJjb21wdXRlIjoiY2xvdWQifQ",
    };
    renderSection();
    const user = userEvent.setup();

    await user.click(screen.getByRole("button", { name: /back to setup/i }));

    expect(routerState.navigate).toHaveBeenCalledWith({
      href: "/onboarding?plan=eyJjb21wdXRlIjoiY2xvdWQifQ",
    });
  });

  /**
   * `returnTo` comes off the address bar. A protocol-relative value is an
   * open redirect, and must fall back to the wizard rather than be honoured.
   */
  it("refuses an off-origin returnTo and falls back to /onboarding", async () => {
    routerState.search = {
      tab: "plans",
      from: "onboarding",
      returnTo: "//evil.example.com/steal",
    };
    renderSection();
    const user = userEvent.setup();

    await user.click(screen.getByRole("button", { name: /back to setup/i }));

    expect(routerState.navigate).toHaveBeenCalledWith({
      to: "/onboarding",
      search: {},
    });
  });

  it("offers no 'Back to setup' to a user who came from settings", () => {
    routerState.search = { tab: "plans" };
    renderSection();

    expect(
      screen.queryByRole("button", { name: /back to setup/i }),
    ).not.toBeInTheDocument();
  });
});
