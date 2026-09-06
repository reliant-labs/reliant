import { render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";

/**
 * The owner's critique, as tests: "we don't distinguish between AI and compute
 * plans very well. it's all too monotone."
 *
 * The page rendered a WALLET and a SUBSCRIPTION in the same card, at the same
 * weight, with the same border — a quantity you deplete and a capacity you
 * rent, drawn from one template. So these assert that the two products are
 * separately addressable REGIONS with different contents, not that they carry
 * particular classes: a class assertion pins styling rather than behaviour and
 * breaks on every visual tweak, while a region with the wrong contents is the
 * actual defect.
 *
 * The other half of the critique — "i don't just want to have adding credit be
 * the way to go" — shows up as: compute owns verbs of its own (change plan, set
 * a ceiling) rather than being a status readout beside the only real button.
 */

const query = (data?: unknown, extra: Record<string, unknown> = {}) => ({
  data,
  isLoading: false,
  error: null,
  refetch: vi.fn(),
  ...extra,
});
const mutation = () => ({
  mutate: vi.fn(),
  mutateAsync: vi.fn(),
  isPending: false,
});

const state = vi.hoisted(() => ({
  sub: undefined as unknown,
  wallet: undefined as unknown,
  usage: undefined as unknown,
  spend: { entries: [], totalSpend: 0 } as unknown,
  walletError: null as unknown,
  usageError: null as unknown,
  loading: false,
}));

const routerState = vi.hoisted(() => {
  const listeners = new Set<() => void>();
  const s = {
    search: {} as Record<string, unknown>,
    subscribe(fn: () => void) {
      listeners.add(fn);
      return () => listeners.delete(fn);
    },
    get() {
      return s.search;
    },
    navigate({ search }: { search?: unknown }) {
      s.search =
        typeof search === "function"
          ? (search as (p: Record<string, unknown>) => Record<string, unknown>)(
              s.search,
            )
          : ((search as Record<string, unknown>) ?? {});
      listeners.forEach((fn) => fn());
    },
    reset() {
      s.search = {};
      listeners.clear();
    },
  };
  return s;
});

vi.mock("@tanstack/react-router", async () => {
  const { useSyncExternalStore } = await import("react");
  return {
    useNavigate: () => routerState.navigate,
    useSearch: () =>
      useSyncExternalStore(routerState.subscribe, routerState.get, routerState.get),
  };
});

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
    useComputeSubscription: () =>
      query(state.sub, { isLoading: state.loading }),
    useWalletOverview: () =>
      query(state.wallet, { isLoading: state.loading, error: state.walletError }),
    useComputeUsage: () =>
      query(state.usage, { isLoading: state.loading, error: state.usageError }),
    usePlans: () => query(undefined),
    useCurrentUserInvoices: () => query(undefined),
    useBillingEmail: () => query(undefined),
    useSetComputeOverage: () => mutation(),
    useCreateCheckoutSession: () => mutation(),
    useCreateWalletTopupSession: () => mutation(),
    useCreateBillingPortalSession: () => mutation(),
    useUpdateBillingEmail: () => mutation(),
  };
});

vi.mock("@/hooks/useReliantAIQueries", async (importOriginal) => {
  const actual = await importOriginal<Record<string, unknown>>();
  return { ...actual, useLLMSpend: () => query(state.spend) };
});

import { BillingSection } from "@/components/Settings/cloud/billing";

/** A subscription whose plan detail is fully populated. */
const HEALTHY_SUB = {
  subscription: {
    overageEnabled: false,
    currentPeriodEnd: { seconds: BigInt(Math.floor(Date.parse("2026-03-14") / 1000)) },
    plan: {
      id: "tier_beta",
      name: "Beta",
      productId: "prod_compute",
      priceCents: 4700n,
      displayOrder: 2,
      structuredLimits: {
        allowedDaemonSizes: ["small", "medium"],
        daemonComputeIncludedMinutes: 2460,
        daemonOveragePerMinuteCents: 0.47,
      },
    },
  },
};

/** The screenshot's state: the row arrived, the detail did not. */
const DEGRADED_SUB = {
  subscription: {
    overageEnabled: false,
    plan: {
      id: "tier_beta",
      name: "Beta",
      productId: "prod_compute",
      priceCents: 4700n,
      displayOrder: 2,
      structuredLimits: {
        allowedDaemonSizes: [],
        daemonComputeIncludedMinutes: 0,
        daemonOveragePerMinuteCents: 0,
      },
    },
  },
};

const USAGE = {
  includedMinutes: 2460,
  usedMinutes: 720,
  overageMinutes: 0,
  estimatedOverageCostCents: 0,
  byDay: [],
  byWorkspace: [],
  grantedMinutesRemaining: 0,
};

function renderSection() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <BillingSection />
    </QueryClientProvider>,
  );
}

const creditBand = () => screen.getByRole("region", { name: /ai credit/i });
const computeBand = () => screen.getByRole("region", { name: /^compute$/i });

beforeEach(() => {
  routerState.reset();
  state.sub = HEALTHY_SUB;
  state.wallet = { overview: { wallet: { balanceUsdNanos: 18_400_000_000n } } };
  state.usage = USAGE;
  state.spend = { entries: [], totalSpend: 0 };
  state.walletError = null;
  state.usageError = null;
  state.loading = false;
});

// ── The two bands are structurally distinct ───────────────────────────

describe("AI credit and compute are two regions, not two identical cards", () => {
  it("gives each product its own labelled region", () => {
    renderSection();
    expect(creditBand()).toBeInTheDocument();
    expect(computeBand()).toBeInTheDocument();
    expect(creditBand()).not.toBe(computeBand());
  });

  /**
   * The owner's complaint, made falsifiable.
   *
   * "Two regions exist" is satisfied by two cards drawn identically — which is
   * precisely what the page did before and precisely what he called monotone.
   * What must hold is that the products get DIFFERENT containers, and that
   * credit is the reservoir rather than the two being swapped.
   *
   * The shape is asserted as a declared contract, not as a class string: a
   * class assertion pins styling rather than behaviour, breaks on every visual
   * tweak, and would still not tell "different" from "reversed".
   */
  it("draws the two products in different containers, credit as the reservoir", () => {
    renderSection();
    expect(creditBand()).toHaveAttribute("data-band-shape", "reservoir");
    expect(computeBand()).toHaveAttribute("data-band-shape", "contract");
  });

  /**
   * The heart of "it's all too monotone". Both products used to render as
   * `<big number> + <label> + <row of controls>` — literally parallel useMemo
   * blocks in the same Card scaffold — so nothing told the reader they were
   * different kinds of thing.
   *
   * Credit is a METER: dollars remaining, drawn as a depletion. Compute is a
   * CAPACITY: hours used OF hours included, with a marked boundary. Asserting
   * each band carries its own metaphor's readout, and NOT the other's, is what
   * makes "they read differently" falsifiable without pinning a class name.
   */
  it("renders credit as a balance meter and compute as a used-of-included capacity", () => {
    renderSection();

    // Credit: dollars, with a meter. No hours anywhere in this band.
    expect(within(creditBand()).getByText("$18.40")).toBeInTheDocument();
    expect(
      within(creditBand()).getByRole("meter", { name: /credit remaining/i }),
    ).toBeInTheDocument();
    expect(within(creditBand()).queryByText(/\bh\b.*included/i)).toBeNull();

    // Compute: hours against a ceiling, with a renewal date. No balance.
    const compute = within(computeBand());
    expect(compute.getByText(/12\.0 h/)).toBeInTheDocument();
    expect(compute.getByText(/41 h included/i)).toBeInTheDocument();
    expect(compute.getByText(/renews/i)).toBeInTheDocument();
    expect(compute.queryByText("$18.40")).toBeNull();
  });

  /**
   * "I don't just want to have adding credit be the way to go." Credit was the
   * page's only real verb, so it read as the primary product while compute —
   * the $20–$160/month recurring one — read as a status readout.
   */
  it("gives compute verbs of its own rather than leaving credit the only action", () => {
    renderSection();
    const compute = within(computeBand());
    expect(compute.getByRole("button", { name: /change plan/i })).toBeInTheDocument();
    // The ceiling control. Its internals belong to the overage-cap work; what
    // this band owes is a live place for it.
    expect(
      compute.getByRole("radio", { name: /stop at my included hours/i }),
    ).toBeEnabled();
  });

  it("keeps both credit actions in the credit band", () => {
    renderSection();
    const credit = within(creditBand());
    const addCredit = within(credit.getByRole("group", { name: /add credit/i }));
    expect(addCredit.getByRole("button", { name: "$25" })).toBeInTheDocument();
    expect(credit.getByRole("button", { name: /redeem/i })).toBeInTheDocument();
  });
});

// ── Runway ────────────────────────────────────────────────────────────

describe("the runway estimate is hedged, and withheld when it would mislead", () => {
  const spendOver = (days: number, totalSpend: number) => ({
    totalSpend,
    entries: Array.from({ length: days }, (_, i) => ({
      model: "m",
      spend: totalSpend / days,
      periodStart: {
        seconds: BigInt(Math.floor(Date.parse(`2026-03-0${i + 1}`) / 1000)),
      },
    })),
  });

  it("states days remaining, hedged, when the sample supports it", () => {
    state.spend = spendOver(6, 12); // $2/day against $18.40
    renderSection();
    expect(
      within(creditBand()).getByText(/~9 days at recent use/i),
    ).toBeInTheDocument();
  });

  /**
   * The suppression case, and the one that matters most: an unavailable
   * estimate renders NOTHING — not "~0 days", not "—". A dash here is a
   * confident-looking value in the place a real one goes, which is exactly the
   * `0 h / mo` defect in another costume.
   */
  it("omits the runway line entirely when there is no spend history", () => {
    state.spend = { entries: [], totalSpend: 0 };
    renderSection();

    const credit = within(creditBand());
    expect(credit.queryByText(/at recent use/i)).toBeNull();
    expect(credit.queryByText(/~0 days/i)).toBeNull();
    // The balance above it still answers the primary question.
    expect(credit.getByText("$18.40")).toBeInTheDocument();
  });

  it("omits the runway when the sample is too short to be stable", () => {
    state.spend = spendOver(2, 12); // two days is not a rate
    renderSection();
    expect(within(creditBand()).queryByText(/at recent use/i)).toBeNull();
  });
});

// ── Degraded states ───────────────────────────────────────────────────

describe("degraded data disables only the controls that depend on it", () => {
  /**
   * §6.4, the non-obvious rule. On the screenshot's plan card `Change plan`
   * needs the CATALOG, not this subscription's stale limits, so it stays live;
   * the overage control's per-minute rate is exactly what is missing, so it
   * does not. Blanket-disabling the card removes the user's escape route,
   * which is usually the thing that would have fixed their problem.
   */
  it("keeps Change plan live while the overage control goes dead", async () => {
    state.sub = DEGRADED_SUB;
    renderSection();

    const compute = within(computeBand());
    expect(compute.getByRole("button", { name: /change plan/i })).toBeEnabled();

    // NOTHING that authorizes spend is reachable — checked across every option
    // and the save, because a control that disabled one radio and left the
    // "no limit" option live would still let a user commit to uncapped charges
    // at a rate we could not read.
    expect(compute.queryAllByRole("radio")).toHaveLength(0);
    expect(compute.queryByRole("button", { name: /save/i })).toBeNull();

    // And it says WHY. A control that simply vanishes reads as a broken page,
    // and the specific wrong answer being avoided here is the overage control's
    // own zero-rate copy — "this plan doesn't offer extra time" — which would
    // be a confident claim about the exact fact that failed to load.
    expect(compute.getByText(/once plan details load/i)).toBeInTheDocument();
    expect(compute.queryByText(/doesn't offer extra time/i)).toBeNull();

    // And the escape route actually works.
    await userEvent.setup().click(
      compute.getByRole("button", { name: /change plan/i }),
    );
    expect(routerState.search.tab).toBe("plans");
  });

  /**
   * Three situations that all rendered as `—` or "not configured". Naming the
   * stale-catalog one is what stops someone auditing config that is already
   * correct.
   */
  it("names the stale-catalog cause instead of showing three dashes", () => {
    state.sub = DEGRADED_SUB;
    renderSection();

    const compute = within(computeBand());
    expect(compute.getByText(/plan details are unavailable/i)).toBeInTheDocument();
    expect(compute.getByText(/control plane/i)).toBeInTheDocument();
    // The plan name and price ARE known, so they still render.
    expect(compute.getByText("Beta")).toBeInTheDocument();
    expect(compute.getByText(/\$47\.00\/mo/)).toBeInTheDocument();
    // What is NOT known is not rendered as a confident zero.
    expect(compute.queryByText(/0 h \/ mo/)).toBeNull();
  });

  it("offers a purposeful empty state when there is no subscription at all", () => {
    state.sub = undefined;
    renderSection();

    const compute = within(computeBand());
    expect(compute.getByText(/no compute plan/i)).toBeInTheDocument();
    expect(compute.getByRole("button", { name: /change plan/i })).toBeEnabled();
    // NOT the stale-catalog advice, which would send a brand new user to look
    // at infrastructure.
    expect(compute.queryByText(/control plane/i)).toBeNull();
  });

  /**
   * "Never offer to spend against a number we could not read." A failed wallet
   * read must not render as $0.00 next to a working Add-credit button.
   */
  it("disables the credit actions when the balance could not be read", () => {
    state.wallet = undefined;
    state.walletError = new Error("boom");
    renderSection();

    const credit = within(creditBand());
    expect(credit.getByText(/couldn't load your balance/i)).toBeInTheDocument();

    // EVERY top-up amount, not one: these are four independent buttons and
    // disabling only the one a test happened to name would leave three live
    // doors onto spending against a balance we could not read.
    const addCredit = within(credit.getByRole("group", { name: /add credit/i }));
    for (const button of addCredit.getAllByRole("button")) {
      expect(button).toBeDisabled();
    }
    // A failed read is not a zero balance, and must not render as one.
    expect(credit.queryByText("$0.00")).toBeNull();
    // Retrying is the one thing still on offer.
    expect(credit.getByRole("button", { name: /retry/i })).toBeEnabled();
  });

  /**
   * Partial failure degrades partially. A usage read that failed must not take
   * the plan's own facts down with it.
   */
  it("keeps the plan readable when only the usage query failed", () => {
    state.usage = undefined;
    state.usageError = new Error("boom");
    renderSection();

    const compute = within(computeBand());
    expect(compute.getByText("Beta")).toBeInTheDocument();
    expect(compute.getByText(/usage unavailable/i)).toBeInTheDocument();
    expect(compute.getByRole("button", { name: /change plan/i })).toBeEnabled();
  });
});

// ── Tab consolidation ─────────────────────────────────────────────────

describe("four tabs become three without losing a surface", () => {
  it("offers three tabs, with invoices folded into usage", () => {
    renderSection();
    const tabs = screen.getAllByRole("tab").map((t) => t.textContent?.trim());
    expect(tabs).toEqual(["Overview", "Change plan", "Usage & invoices"]);
  });

  /**
   * The merge must not delete a surface. Both halves have to be reachable from
   * the one tab — asserting only that the tab exists would pass against a
   * merge that dropped the invoice table entirely.
   */
  it("reaches both invoices and usage from the merged tab", async () => {
    renderSection();
    await userEvent.setup().click(screen.getByRole("tab", { name: /usage & invoices/i }));

    expect(
      screen.getByRole("heading", { name: /^invoices$/i }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("heading", { name: /compute usage/i }),
    ).toBeInTheDocument();
  });

  /**
   * External links carry `?tab=invoices`. Dropping the id from the enum would
   * make those 404 into the default tab silently, so it stays an accepted
   * INBOUND value that resolves to the merged surface.
   */
  it("resolves an inbound ?tab=invoices link to the merged tab", () => {
    routerState.search = { tab: "invoices" };
    renderSection();

    expect(
      screen.getByRole("tab", { name: /usage & invoices/i }),
    ).toHaveAttribute("aria-selected", "true");
    expect(
      screen.getByRole("heading", { name: /^invoices$/i }),
    ).toBeInTheDocument();
  });
});

// ── Payment methods ───────────────────────────────────────────────────

describe("payment methods are Stripe's to decide", () => {
  /**
   * Which methods are available depends on the browser, the device and on
   * Stripe Dashboard domain registration. A static logo row is a promise we
   * cannot keep — and PayPal in particular is unavailable for subscriptions
   * entirely, so naming it beside a plan is wrong twice over.
   */
  it("names no payment brand anywhere on the overview", () => {
    renderSection();
    const text = document.body.textContent ?? "";
    for (const brand of [
      "PayPal",
      "Apple Pay",
      "Google Pay",
      "Visa",
      "Mastercard",
      "Amex",
      "Link",
    ]) {
      expect(text).not.toContain(brand);
    }
    expect(screen.queryByRole("img", { name: /paypal|visa|mastercard/i })).toBeNull();
  });
});
