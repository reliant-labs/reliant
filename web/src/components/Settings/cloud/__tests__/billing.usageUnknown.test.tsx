import { render, screen, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";

/**
 * "The server did not measure your usage" and "you ran nothing this period"
 * are different facts, and the billing page must not draw them the same way.
 *
 * The incident: GetCurrentUserComputeUsage was a stub returning
 * used_minutes = 0 on a 200 response. `usageUnavailable` only tripped on
 * `usageQ.error`, so the unknown flowed through as a measurement and the page
 * rendered "0.0 h used" in the compute band's most emphatic style — and
 * StatusLine hoisted it to the most prominent line on the page.
 *
 * That is worse than the `0 h / mo` complaint this redesign was built to fix.
 * `0 h / mo` was alarming; "0.0 h used" is REASSURING and wrong in the
 * direction that costs money: the daemon start gate enforces against real
 * metered usage, so a user could be refused a machine while billing showed
 * them at 0% of their allowance.
 *
 * The server now says which it is, via `usageMeasured`. These tests exist to
 * make sure the client cannot collapse the two again — so every case below is
 * paired: the unmeasured rendering AND the genuine-zero rendering, asserted
 * to differ. A test that only checked the unmeasured case would pass against
 * a client that suppressed the number unconditionally, which would lose the
 * real zero instead.
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
  spend: { entries: [], totalSpend: 0, sampleDays: 0 } as unknown,
  usageError: null as unknown,
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
      useSyncExternalStore(
        routerState.subscribe,
        routerState.get,
        routerState.get,
      ),
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
    useComputeSubscription: () => query(state.sub),
    useWalletOverview: () => query(state.wallet),
    useComputeUsage: () => query(state.usage, { error: state.usageError }),
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

const HEALTHY_SUB = {
  subscription: {
    overageEnabled: false,
    currentPeriodEnd: {
      seconds: BigInt(Math.floor(Date.parse("2026-03-14") / 1000)),
    },
    plan: {
      id: "plan_compute_small",
      name: "Compute Small",
      productId: "prod_compute",
      priceCents: 2000n,
      displayOrder: 1,
      structuredLimits: {
        allowedDaemonSizes: ["small"],
        daemonComputeIncludedMinutes: 1980, // 33 h
        daemonOveragePerMinuteCents: 0.47,
      },
    },
  },
};

/** What the server sends once it has actually read the heartbeat buckets. */
const measured = (usedMinutes: number) => ({
  includedMinutes: 1980,
  usedMinutes,
  overageMinutes: 0,
  estimatedOverageCostCents: 0,
  byDay: [],
  byWorkspace: [],
  grantedMinutesRemaining: 0,
  usageMeasured: true,
});

/**
 * What a server that could not measure sends. Note it is byte-for-byte the
 * old stub apart from the flag — which is the whole point: without the flag
 * this is indistinguishable from `measured(0)`.
 */
const UNMEASURED = { ...measured(0), usageMeasured: false };

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

const computeBand = () => screen.getByRole("region", { name: /^compute$/i });

beforeEach(() => {
  routerState.reset();
  state.sub = HEALTHY_SUB;
  state.wallet = {
    overview: { wallet: { balanceUsdNanos: 18_400_000_000n } },
  };
  state.usage = UNMEASURED;
  state.spend = { entries: [], totalSpend: 0, sampleDays: 0 };
  state.usageError = null;
});

describe("unmeasured usage is never rendered as a measured zero", () => {
  it("does not print an hours-used figure when the server did not measure", () => {
    state.usage = UNMEASURED;
    renderSection();

    // The exact string from the incident. `0.0 h` must not appear anywhere in
    // the band while the server is guessing.
    expect(within(computeBand()).queryByText(/0\.0 h/)).not.toBeInTheDocument();
    expect(
      within(computeBand()).queryByText(/0\.0 h used/i),
    ).not.toBeInTheDocument();
  });

  it("says the usage is unavailable rather than staying silent about it", () => {
    state.usage = UNMEASURED;
    renderSection();

    // Withholding the number is necessary but not sufficient: a band that
    // simply omits it leaves a user to assume the blank means zero. The
    // unknown has to be stated.
    expect(
      within(computeBand()).getByText(/usage unavailable|couldn't (load|measure)/i),
    ).toBeInTheDocument();
  });

  it("still shows what the plan includes, which does not depend on metering", () => {
    state.usage = UNMEASURED;
    renderSection();

    // Partial failure degrades partially. Included hours come from the plan,
    // so blanking them too would throw away a fact we hold.
    expect(
      within(computeBand()).getByText(/your plan includes 33 h/i),
    ).toBeInTheDocument();
  });

  // THE DISCRIMINATOR. If this and the first test cannot both pass, the client
  // has collapsed "unknown" and "zero" again — in whichever direction.
  it("DOES print 0.0 h for a user who genuinely ran nothing", () => {
    state.usage = measured(0);
    renderSection();

    expect(within(computeBand()).getByText(/0\.0 h/)).toBeInTheDocument();
    expect(
      within(computeBand()).queryByText(/usage unavailable/i),
    ).not.toBeInTheDocument();
  });

  it("prints real hours for a user who ran machines", () => {
    state.usage = measured(720); // 12 h
    renderSection();

    expect(within(computeBand()).getByText(/12\.0 h/)).toBeInTheDocument();
  });
});

describe("the page's most prominent line does not carry an unmeasured number", () => {
  /**
   * StatusLine sits at the top of the Overview and reads e.g.
   * "Compute Small · 0.0 h of 33 h included · $18.40 credit remaining".
   * That was the single most valuable place on the page and it was spending
   * it on something untrue.
   */
  it("omits the hours clause when usage was not measured", () => {
    state.usage = UNMEASURED;
    const { container } = renderSection();

    expect(container.textContent).not.toMatch(/0\.0 h of/);
  });

  it("includes the hours clause when usage WAS measured, even at zero", () => {
    state.usage = measured(0);
    const { container } = renderSection();

    expect(container.textContent).toMatch(/0\.0 h of 33 h included/);
  });
});

describe("an errored usage query and an unmeasured one both withhold", () => {
  // The pre-existing degraded path must keep working: the fix adds a second
  // route to "unknown", it does not replace the first.
  it("withholds when the query itself failed", () => {
    state.usage = undefined;
    state.usageError = new Error("boom");
    renderSection();

    expect(within(computeBand()).queryByText(/0\.0 h/)).not.toBeInTheDocument();
  });
});
