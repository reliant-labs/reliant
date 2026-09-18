/**
 * The purchase surface: pick a MACHINE, pay without leaving the page.
 *
 * ── One list, because there is one decision ───────────────────────────
 *
 * This tab used to stack three controls for that decision — a size
 * radiogroup, a four-across grid of plan cards, and, once a card was clicked,
 * `ComputeSubscriptionCheckout` mounted ABOVE the grid carrying its own copy
 * of the same machine list. The cards stayed. So choosing produced two lists
 * of the same four things that did not agree with each other.
 *
 * The size filter was the other half and it was information-free, not merely
 * redundant. Every plan's `allowed_daemon_sizes` contains every cheaper
 * plan's, so filtering by size could only ever dim the cheaper cards — it
 * could not exclude a single plan that would otherwise have been a real
 * alternative. Size and plan are ONE axis, and `smallestPlanAllowingSize` is
 * the whole of it.
 *
 * What is pinned here now, and each replaces something that used to be true
 * and was wrong:
 *
 * 1. **One machine list, no second copy and no filter.** Asserted as an
 *    ABSENCE as well as a presence: no radiogroup, and exactly one row per
 *    size. An absence assertion is what catches the old surface being
 *    restored beside the new one, which is the shape of the original defect.
 *
 * 2. **Each row names a machine, its specs and its price.** Not a plan name,
 *    and specifically not an hours figure — the allowance does not vary by
 *    size, and the fixture keeps stale per-plan minute values alive on
 *    purpose so a reintroduced per-row line renders three different numbers
 *    and fails.
 *
 * 3. **Buying never navigates.** The old grid called `openCheckout`, which set
 *    `window.location.href` to a hosted Stripe URL — on desktop that escaped
 *    to the system browser, on mobile it backgrounded the tab. Asserted three
 *    ways, because a surface can mount the panel and still redirect.
 *
 * 4. **Prices come off the wire.** The fixture is deliberately REPRICED — the
 *    catalog's real tiers are $15/$35/$79 and these are not — so a hardcoded
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
    useWalletAutoRecharge: () => query(undefined),
    useSetWalletAutoRecharge: () => mutation(),
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
// place. The checkout's own behaviour (Elements, plan switching, 3DS,
// settlement) is covered in components/Billing.
vi.mock("@/components/Billing/ComputeSubscriptionCheckout", () => ({
  ComputeSubscriptionCheckout: ({
    selectedPlanId,
  }: {
    selectedPlanId?: string;
  }) => (
    <div
      data-testid="compute-checkout"
      data-kind="compute_plan"
      data-plan-id={selectedPlanId ?? ""}
    >
      compute checkout
    </div>
  ),
}));

// Our own top-up page, stubbed for the same reason: this file is about which
// purchase the SURFACE mounts, not about how the payment form behaves. Its
// own behaviour — Elements, 3DS, settlement — is covered in components/Billing.
vi.mock("@/components/Billing/WalletTopupCheckout", () => ({
  // `amountCents`, not `defaultAmountCents`: the panel no longer seeds a picker
  // of its own, it is TOLD what is being bought. The band's preset row is the
  // single amount control.
  WalletTopupCheckout: ({ amountCents }: { amountCents?: number }) => (
    <div
      data-testid="wallet-topup-checkout"
      data-amount={String(amountCents ?? "")}
    >
      wallet top-up
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

/**
 * Every machine row on the tab, in render order.
 *
 * Matched on ACCESSIBLE NAME rather than `textContent`. The row stacks its
 * name, its specs and its price in sibling spans, so `textContent` runs them
 * together as "Small2 GB RAM · 0.5 CPU$23.00/mo" with no separator to anchor
 * against; the accessible name normalises that to "Small 2 GB RAM · 0.5 CPU
 * $23.00/mo". It is also the string a screen reader announces, which is the
 * one worth asserting about.
 *
 * `hidden: true` because a disabled row is still in the list — the current
 * plan's row is marked and unbuyable, not removed.
 */
function machineRows() {
  return screen.queryAllByRole("button", {
    name: /^(Small|Medium|Large|XL) /,
    hidden: true,
  });
}

function machineRow(size: string) {
  return screen.getByRole("button", {
    name: new RegExp(`^${size} `),
    hidden: true,
  });
}

describe("the machine list", () => {
  /**
   * ONE row per size the catalog sells. Not one per plan, and not one per
   * plan PLUS one per size, which is what rendering the checkout's list
   * beside the tab's own produced.
   */
  it("shows one row per machine size, cheapest-first", async () => {
    const user = userEvent.setup();
    await openPlans(user);

    const rows = machineRows();
    expect(rows).toHaveLength(3);
    // Identity AND order, by comparing the collected rows against the rows
    // looked up individually. Reading names out of `textContent` would give
    // "Small2" — the row's sibling spans run together with no separator, which
    // is what machineRows() exists to work around.
    expect(rows).toEqual([
      machineRow("Small"),
      machineRow("Medium"),
      machineRow("Large"),
    ]);
  });

  /**
   * The filter is GONE, and its absence is the assertion.
   *
   * A strictly-nested ladder cannot be filtered — every plan's size set
   * contains every cheaper plan's, so the control could only dim the cheaper
   * rows. Pinning the absence is what catches it being restored alongside the
   * list rather than instead of it.
   */
  it("offers no size filter, because the ladder has nothing to filter", async () => {
    const user = userEvent.setup();
    await openPlans(user);

    expect(screen.queryByRole("radiogroup")).not.toBeInTheDocument();
    expect(screen.queryByText(/what size machines do you need/i)).not.toBeInTheDocument();
  });

  /**
   * No plan in this catalog runs XL, so XL is not a machine anyone can buy
   * here. The list is the catalog's union of sizes, not the client's enum.
   */
  it("omits a size no plan in the catalog runs", async () => {
    const user = userEvent.setup();
    await openPlans(user);

    expect(
      screen.queryByRole("button", { name: /^XL /, hidden: true }),
    ).not.toBeInTheDocument();
  });

  it("prices each row from the wire, not from a client constant", async () => {
    const user = userEvent.setup();
    await openPlans(user);

    // $23 / $47 / $91 exist in no table the client ever had. Each is the
    // CHEAPEST plan that runs that size — Alpha runs small, Beta adds medium,
    // Gamma adds large — which is the whole of "size and plan are one axis".
    expect(machineRow("Small")).toHaveTextContent("$23.00/mo");
    expect(machineRow("Medium")).toHaveTextContent("$47.00/mo");
    expect(machineRow("Large")).toHaveTextContent("$91.00/mo");
  });

  /**
   * A machine row says what the machine IS. The owner's note was that the
   * vertical rows should "also contain the details... like size and hours",
   * and the specs are the half that genuinely varies per row.
   */
  it("names what each machine reserves", async () => {
    const user = userEvent.setup();
    await openPlans(user);

    expect(machineRow("Small")).toHaveTextContent("2 GB RAM · 0.5 CPU");
    expect(machineRow("Medium")).toHaveTextContent("4 GB RAM · 1 CPU");
    expect(machineRow("Large")).toHaveTextContent("8 GB RAM · 2 CPU");
  });

  /**
   * The other half — the hours — does NOT vary, and must not be printed per
   * row.
   *
   * control-plane grants 9600 minutes at every size. The fixture keeps the
   * hazard alive on purpose: its plans carry 1200 / 2600 / 5200 minutes, so a
   * per-row line would render "20 hours" beside "43 hours" beside "87 hours"
   * and state a product claim nobody made — that a bigger machine buys more
   * time. That is exactly the defect that shipped once from a lagging dev
   * database, and PlanTiles.tsx carries the long version.
   */
  it("puts no hours figure on any machine row", async () => {
    const user = userEvent.setup();
    await openPlans(user);

    for (const size of ["Small", "Medium", "Large"]) {
      expect(machineRow(size)).not.toHaveTextContent(/hours/i);
      expect(machineRow(size)).not.toHaveTextContent(/included/i);
    }
  });

  /**
   * It is still SAID — once, beneath the list, and still read from the
   * catalog so it tracks 9600 if control-plane changes it.
   */
  it("states the included hours once, as a property of every size", async () => {
    const user = userEvent.setup();
    await openPlans(user);

    const note = screen.getByTestId("plans-hours-note");
    // 1200 min = 20 h, from the fixture's own catalog — not a constant.
    expect(note).toHaveTextContent(/20 machine hours/i);
    expect(note).toHaveTextContent(/every size/i);
    expect(screen.getAllByTestId("plans-hours-note")).toHaveLength(1);
  });

  /**
   * The overage RATE is the one uniform-looking fact that is not uniform:
   * control-plane prices it per plan (0.25 / 0.6 / 1.0 / 1.7 cents per minute
   * up the real ladder, and 0.25 / 0.47 / 0.91 in this fixture). Quoting one
   * beneath a list of three machines would assert a uniformity that does not
   * exist, in the currency the user is about to be charged in. The policy is
   * stated without a number; the figure belongs in the checkout, where one
   * machine is selected.
   */
  it("states the pause policy without quoting one machine's overage rate", async () => {
    const user = userEvent.setup();
    await openPlans(user);

    expect(screen.getByText(/machines pause until next month/i)).toBeInTheDocument();
    expect(screen.queryByText(/\$0\.15\/hour/)).not.toBeInTheDocument();
    expect(screen.queryByText(/\$0\.28\/hour/)).not.toBeInTheDocument();
  });

  /** Uniform too, so it is one sentence rather than three. */
  it("states the burst ceiling once, beneath the list", async () => {
    const user = userEvent.setup();
    await openPlans(user);

    const note = screen.getByTestId("plans-burst-note");
    expect(note).toHaveTextContent(/4× the CPU/);
    expect(note).toHaveTextContent(/2× the memory/);
    for (const size of ["Small", "Medium", "Large"]) {
      expect(machineRow(size)).not.toHaveTextContent(/burst/i);
    }
  });
});

describe("the plan the user is already on", () => {
  /**
   * A user switching machines needs to see which one they have — and must not
   * be able to buy it again. Marked and disabled rather than removed: a row
   * missing from the list makes the list of machines they are choosing from
   * silently different from the list of machines.
   */
  it("marks the current plan's machine and refuses to re-buy it", async () => {
    subState.current = { subscription: { plan: PLANS[1] } };
    const user = userEvent.setup();
    await openPlans(user);

    const medium = machineRow("Medium");
    expect(medium).toHaveTextContent(/current plan/i);
    expect(medium).toBeDisabled();

    await user.click(medium);
    expect(screen.queryByTestId("compute-checkout")).not.toBeInTheDocument();
  });

  it("leaves the other machines buyable", async () => {
    subState.current = { subscription: { plan: PLANS[1] } };
    const user = userEvent.setup();
    await openPlans(user);

    expect(machineRow("Large")).toBeEnabled();
    expect(machineRow("Large")).toHaveTextContent("$91.00/mo");
  });
});

describe("purchasing happens in place", () => {
  /**
   * Picking Medium buys Beta — the cheapest plan that runs a Medium machine.
   * That mapping is the whole reason the size filter could go: the plan is
   * derived from the machine, never chosen beside it.
   */
  it("mounts the checkout for the cheapest plan that runs the chosen machine", async () => {
    const user = userEvent.setup();
    await openPlans(user);
    expect(screen.queryByTestId("compute-checkout")).not.toBeInTheDocument();

    await user.click(machineRow("Medium"));

    const panel = screen.getByTestId("compute-checkout");
    expect(panel).toHaveAttribute("data-kind", "compute_plan");
    expect(panel).toHaveAttribute("data-plan-id", "tier_beta");
  });

  /**
   * The defect the owner reported, pinned as an absence.
   *
   * > "the cards are the only first option, but when clicked pop these
   * > options up. however the cards are still there at the bottom."
   *
   * The checkout carries its own machine list, so the tab's list must be GONE
   * while it is open — otherwise the same four machines are on screen twice
   * and switching in one does not move the other. This is the assertion that
   * fails if the two are ever stacked again.
   */
  it("replaces the machine list with the checkout rather than stacking them", async () => {
    const user = userEvent.setup();
    await openPlans(user);
    expect(machineRows().length).toBeGreaterThan(0);

    await user.click(machineRow("Medium"));

    expect(screen.getByTestId("compute-checkout")).toBeInTheDocument();
    expect(machineRows()).toHaveLength(0);
    expect(screen.queryByTestId("plans-hours-note")).not.toBeInTheDocument();
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

    await user.click(machineRow("Medium"));

    expect(window.location.href).toBe(before);
    expect(stripeNav.open).not.toHaveBeenCalled();
    expect(stripeNav.buildUrls).not.toHaveBeenCalled();
    expect(routerState.navigate).not.toHaveBeenCalled();
    expect(checkoutCalls.mutate).not.toHaveBeenCalled();
  });

  it("lets the user back out of a purchase without buying", async () => {
    const user = userEvent.setup();
    await openPlans(user);

    await user.click(machineRow("Medium"));
    expect(screen.getByTestId("compute-checkout")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: /cancel/i }));
    expect(screen.queryByTestId("compute-checkout")).not.toBeInTheDocument();
    // And the list comes back, so cancelling returns the user to the choice
    // rather than to an empty tab.
    expect(machineRows().length).toBeGreaterThan(0);
  });
});

describe("adding AI credit", () => {
  /**
   * The other half of "one place to spend money": credit top-ups buy through
   * the same panel, so neither purchase leaves the page.
   */
  it("mounts our own top-up page for the chosen amount", async () => {
    renderSection();
    const user = userEvent.setup();

    await user.click(screen.getByRole("button", { name: /^\$25$/ }));

    // OUR page now, not Stripe's whole checkout in an iframe. What matters to
    // this file is unchanged and is what it still asserts: the purchase mounts
    // IN PLACE, carrying the amount the user picked.
    const panel = screen.getByTestId("wallet-topup-checkout");
    expect(panel).toHaveAttribute("data-amount", "2500");
    expect(screen.queryByTestId("compute-checkout")).not.toBeInTheDocument();
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
