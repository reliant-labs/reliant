/**
 * App hosting on a phone: the billing screen mounts desktop's hosting card
 * (UsageBillingPanel, unmocked here) so an org owner can authorize hosting
 * and continue to checkout, follows the server's `enabled`, opens scrolled to
 * the card for `?tab=hosting`, and asks before acting in another org.
 */
import { fireEvent, render, screen, waitFor, within } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import { billingHandlers } from "@/test/billingTransport";

const query = (data?: unknown) => ({ data, isLoading: false, error: null, refetch: vi.fn() });
const mocks = vi.hoisted(() => ({
  read: vi.fn(),
  checkout: vi.fn(),
  open: vi.fn(),
  select: vi.fn(),
  org: { id: "org-a", name: "Acme", role: "owner" },
}));

// The machine half of the screen, as MobileBillingScreen.test.tsx stubs it;
// the hosting checkout mutation stays the real one.
vi.mock("@/hooks/useCloudBillingQueries", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  useComputeSubscription: () => query(undefined),
  useWalletOverview: () => query(undefined),
  useComputeUsage: () => query(undefined),
  usePlans: () => query({ plans: [] }),
}));
vi.mock("@/hooks/useOrgs", () => ({
  useActiveOrg: () => ({
    orgs: [mocks.org, { id: "org-b", name: "Side Project", role: "owner" }],
    active: mocks.org,
    activeOrgId: mocks.org.id,
    isLoading: false,
    error: null,
    refetch: vi.fn(),
    select: mocks.select,
  }),
}));
vi.mock("@/services/controlPlane/config", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  hasControlPlane: true,
}));
// The hosting usage summary finds no method on the client, as on a server without it.
vi.mock("@/services/controlPlane/client", () => ({ getControlPlaneClient: () => ({}) }));
vi.mock("@/lib/stripeCheckout", async (importOriginal) => ({
  ...(await importOriginal<Record<string, unknown>>()),
  openCheckout: mocks.open,
}));

const { MobileBillingScreen } = await import("../MobileBillingScreen");

beforeEach(() => {
  vi.clearAllMocks();
  sessionStorage.clear();
  mocks.read.mockResolvedValue({ enabled: true, policyVersion: "p1", rates: [{ displayName: "CPU", priceCents: "9", unit: "vCPU-hour" }] });
  mocks.checkout.mockResolvedValue({ checkoutUrl: "https://checkout.stripe.test/session" });
  mocks.open.mockResolvedValue(null);
  setTransport(mockTransport(billingHandlers({ getCurrentUserUsageBilling: (r) => mocks.read(r), createCurrentUserUsageBillingCheckout: (r) => mocks.checkout(r) })));
});
afterEach(() => {
  setTransport(null);
  // jsdom has no scrollIntoView; the scroll tests install a spy.
  delete (Element.prototype as { scrollIntoView?: unknown }).scrollIntoView;
});

function renderScreen(props: Partial<Parameters<typeof MobileBillingScreen>[0]> = {}) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MobileBillingScreen onBack={vi.fn()} {...props} />
    </QueryClientProvider>,
  );
}

describe("MobileBillingScreen app hosting", () => {
  it("offers an org owner the hosting checkout when the server enables it", async () => {
    renderScreen();
    const hosting = screen.getByRole("region", { name: "App hosting" });
    fireEvent.change(await within(hosting).findByLabelText(/Monthly app hosting admission budget/), { target: { value: "25.00" } });
    fireEvent.click(within(hosting).getByRole("checkbox"));
    fireEvent.click(within(hosting).getByRole("button", { name: "Authorize and continue to checkout" }));

    await waitFor(() => expect(mocks.checkout).toHaveBeenCalledWith(expect.objectContaining({ orgId: "org-a", policyVersion: "p1", infraBudgetCents: 2500n })));
    // It returns to the hosting tab, which a phone opens on this card.
    const { successUrl } = mocks.checkout.mock.calls[0][0] as { successUrl: string };
    const url = new URL(successUrl);
    expect(url.pathname).toBe("/settings/billing");
    expect(url.searchParams.get("tab")).toBe("hosting");
    await waitFor(() => expect(mocks.open).toHaveBeenCalledWith("https://checkout.stripe.test/session", successUrl));
  });

  it("says hosting billing is not available while the server has it off", async () => {
    mocks.read.mockResolvedValue({ enabled: false });
    renderScreen();
    const hosting = screen.getByRole("region", { name: "App hosting" });
    expect(await within(hosting).findByText("App hosting billing isn’t available yet.")).toBeInTheDocument();
    expect(within(hosting).queryByRole("button", { name: /Authorize and continue/ })).not.toBeInTheDocument();
  });

  it("scrolls to the hosting card when opened for it", async () => {
    const scrollIntoView = vi.fn();
    Element.prototype.scrollIntoView = scrollIntoView;
    renderScreen({ focusHosting: true });
    await waitFor(() => expect(scrollIntoView).toHaveBeenCalledTimes(1));
    expect(scrollIntoView.mock.contexts[0]).toBe(screen.getByRole("region", { name: "App hosting" }));
  });

  it("does not scroll on an ordinary visit", () => {
    const scrollIntoView = vi.fn();
    Element.prototype.scrollIntoView = scrollIntoView;
    renderScreen();
    expect(scrollIntoView).not.toHaveBeenCalled();
  });

  it("asks before acting for another org a link names, and authorizes nothing until then", async () => {
    const onDismissHostingOrg = vi.fn();
    renderScreen({ hostingOrgId: "org-b", onDismissHostingOrg });
    const hosting = screen.getByRole("region", { name: "App hosting" });
    expect(within(hosting).getByTestId("billing-org-gate")).toHaveTextContent("This billing link is for Side Project");
    expect(within(hosting).queryByRole("checkbox")).not.toBeInTheDocument();
    expect(mocks.read).not.toHaveBeenCalled();

    fireEvent.click(within(hosting).getByTestId("billing-org-switch"));
    expect(mocks.select).toHaveBeenCalledWith("org-b");
    expect(onDismissHostingOrg).toHaveBeenCalled();
  });
});
