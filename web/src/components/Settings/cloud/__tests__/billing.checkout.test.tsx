/**
 * The billing page's URL contract: which tab renders, and what the user sees
 * on the way back from Stripe.
 *
 * Both were unobservable before. The tab was `useState`, so no link could
 * target it and no round-trip could restore it; and `successUrl` and
 * `cancelUrl` were both `window.location.href`, so a completed purchase and an
 * abandoned one returned to the identical URL and rendered identically.
 */
import { render, screen } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, describe, expect, it, vi } from "vitest";

const query = (data?: unknown) => ({
  data,
  isLoading: false,
  error: null,
  refetch: vi.fn(),
});
const mutation = () => ({ mutate: vi.fn(), mutateAsync: vi.fn(), isPending: false });

const routerState = vi.hoisted(() => ({
  search: {} as Record<string, unknown>,
  navigate: vi.fn(),
}));
const subState = vi.hoisted(() => ({ current: undefined as unknown }));

vi.mock("@tanstack/react-router", () => ({
  useNavigate: () => routerState.navigate,
  useSearch: () => routerState.search,
}));

vi.mock("@/hooks/useCloudBillingQueries", async (importOriginal) => {
  const actual = await importOriginal<Record<string, unknown>>();
  return {
    ...actual,
    cloudBillingKeys: {
      all: ["cloud-billing"],
      computeSubscription: ["cloud-billing", "compute-subscription"],
      walletOverview: ["cloud-billing", "wallet-overview"],
      computeUsage: (period: string) => ["cloud-billing", "compute-usage", period],
      plans: ["cloud-billing", "plans"],
      invoices: ["cloud-billing", "invoices"],
      billingEmail: ["cloud-billing", "billing-email"],
    },
    useComputeSubscription: () => query(subState.current),
    useWalletOverview: () => query(undefined),
    useComputeUsage: () => query(undefined),
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

import { BillingSection } from "@/components/Settings/cloud/billing";

function renderSection() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <BillingSection />
    </QueryClientProvider>,
  );
}

beforeEach(() => {
  vi.clearAllMocks();
  routerState.search = {};
  subState.current = undefined;
});

describe("billing tab deep-linking", () => {
  it("renders Overview when no tab is named", () => {
    renderSection();

    // Overview is now the two product bands. Asserting on both is what makes
    // this a check that Overview rendered rather than that SOMETHING did — the
    // old single "Balance" heading was equally satisfied by a page that had
    // lost the compute half entirely, which is the defect the redesign fixed.
    expect(screen.getByRole("region", { name: /ai credit/i })).toBeInTheDocument();
    expect(screen.getByRole("region", { name: /^compute$/i })).toBeInTheDocument();
  });

  it("renders the Plans tab when the URL asks for it", () => {
    // This is what "Set up billing" now links to. Before the tab moved into the
    // router, a link could not express it and the user landed on a dashboard.
    routerState.search = { tab: "plans" };
    renderSection();

    expect(
      screen.getByRole("heading", { level: 3, name: /no plans available/i }),
    ).toBeInTheDocument();
  });

  it("renders the Usage tab when the URL asks for it", () => {
    routerState.search = { tab: "usage" };
    renderSection();

    expect(
      screen.getByRole("heading", { level: 3, name: /no usage data/i }),
    ).toBeInTheDocument();
  });
});

describe("return from Stripe", () => {
  it("shows a pending confirmation for ?checkout=success before the webhook lands", () => {
    // The success marker is a client-side claim. Until the SERVER reports the
    // subscription, the honest state is "confirming", never "you're on Pro".
    routerState.search = { checkout: "success", planId: "compute-standard" };
    renderSection();

    expect(screen.getByText(/confirming your payment/i)).toBeInTheDocument();
    expect(screen.queryByText(/plan is active/i)).not.toBeInTheDocument();
  });

  it("confirms the plan by name once the subscription query reports it", () => {
    routerState.search = { checkout: "success", planId: "compute-standard" };
    subState.current = {
      subscription: { plan: { id: "compute-standard", name: "Standard" } },
    };
    renderSection();

    expect(screen.getByText(/your standard plan is active/i)).toBeInTheDocument();
    expect(screen.queryByText(/confirming your payment/i)).not.toBeInTheDocument();
  });

  it("says nothing was charged for ?checkout=cancelled, and shows no success state", () => {
    routerState.search = { checkout: "cancelled" };
    renderSection();

    expect(screen.getByText(/checkout wasn't completed/i)).toBeInTheDocument();
    expect(screen.getByText(/nothing was charged/i)).toBeInTheDocument();
    expect(screen.queryByText(/confirming your payment/i)).not.toBeInTheDocument();
    expect(screen.queryByText(/plan is active/i)).not.toBeInTheDocument();
  });

  it("renders no return state at all on an ordinary visit", () => {
    renderSection();

    expect(screen.queryByText(/confirming your payment/i)).not.toBeInTheDocument();
    expect(screen.queryByText(/checkout wasn't completed/i)).not.toBeInTheDocument();
  });

  // The onboarding dead end: returnTo hard-coded /settings/billing, so a user
  // who detoured from the compute step had no route back into the wizard.
  it("offers a way back into onboarding when the user came from it", () => {
    routerState.search = { checkout: "success", from: "onboarding" };
    renderSection();

    expect(
      screen.getByRole("button", { name: /back to setup/i }),
    ).toBeInTheDocument();
  });

  it("does not offer 'back to setup' to a user who came from settings", () => {
    routerState.search = { checkout: "success" };
    renderSection();

    expect(
      screen.queryByRole("button", { name: /back to setup/i }),
    ).not.toBeInTheDocument();
  });
});
