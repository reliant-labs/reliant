/**
 * MobileBillingScreen — simple cards, not the desktop four-tab dashboard.
 * Mocks `useCloudBillingQueries` the same way `billing.test.tsx` does so it
 * renders deterministically with no network.
 */

import { render, screen, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";

const query = (data?: unknown) => ({
  data,
  isLoading: false,
  error: null,
  refetch: vi.fn(),
});
const mutate = vi.fn();
const mutation = () => ({ mutate, mutateAsync: vi.fn(), isPending: false });

vi.mock("../../../hooks/useCloudBillingQueries", () => ({
  useComputeSubscription: () => query(undefined),
  useWalletOverview: () => query(undefined),
  useComputeUsage: () => query(undefined),
  // The wire shape as it is actually served: price and limits are typed
  // fields, not a `limits` JSON string. A plan with no priceCents is not
  // purchasable, so the fixture has to carry one or the Upgrade button is
  // correctly absent.
  // A catalog with MORE THAN ONE plan, repriced away from the real tiers and
  // with ids the client has never heard of. Both matter: a one-plan fixture
  // cannot tell "offers a choice" from "auto-picks the only option", and
  // $23/$47 match no constant that ever lived in billingUtils, so a
  // reintroduced id-keyed price table cannot satisfy these by coincidence.
  usePlans: () =>
    query({
      plans: [
        {
          id: "tier_beta",
          name: "Beta",
          priceCents: 4700n,
          displayOrder: 2,
          structuredLimits: {
            allowedDaemonSizes: ["small", "medium"],
            daemonComputeIncludedMinutes: 2600,
            daemonOveragePerMinuteCents: 0.47,
          },
        },
        {
          id: "tier_alpha",
          name: "Alpha",
          priceCents: 2300n,
          displayOrder: 1,
          structuredLimits: {
            allowedDaemonSizes: ["small"],
            daemonComputeIncludedMinutes: 1200,
            daemonOveragePerMinuteCents: 0.25,
          },
        },
      ],
    }),
  useCreateCheckoutSession: () => mutation(),
  useCreateWalletTopupSession: () => mutation(),
  isCheckoutIdentityRequired: () => false,
}));

// The panel is exercised directly in components/Billing; here it is stubbed so
// this file keeps testing the SCREEN — that tapping Upgrade opens checkout in
// place for the right plan, rather than navigating away.
vi.mock("../../Billing/EmbeddedCheckoutPanel", () => ({
  EmbeddedCheckoutPanel: ({
    request,
  }: {
    request: { kind: string; planId?: string };
  }) => (
    <div data-testid="embedded-checkout" data-plan-id={request.planId}>
      embedded checkout
    </div>
  ),
}));

const { MobileBillingScreen } = await import("../MobileBillingScreen");

function renderScreen(onBack = vi.fn()) {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  return render(
    <QueryClientProvider client={client}>
      <MobileBillingScreen onBack={onBack} />
    </QueryClientProvider>,
  );
}

describe("MobileBillingScreen", () => {
  // The two products are now two labelled regions rather than two
  // identically-drawn cards. Asserting on the region is what makes this a
  // check that the product's own band rendered, rather than that some card
  // somewhere happened to contain the words.
  it("shows an AI credit band", () => {
    renderScreen();
    expect(screen.getByRole("region", { name: /ai credit/i })).toBeInTheDocument();
  });

  it("shows a compute band with no compute plan by default", () => {
    renderScreen();
    const compute = screen.getByRole("region", { name: /^compute$/i });
    expect(compute).toBeInTheDocument();
    expect(within(compute).getByText("No compute plan")).toBeInTheDocument();
  });

  it("does not render the desktop tab bar or invoices/usage detail tabs", () => {
    renderScreen();
    expect(screen.queryByRole("button", { name: /^invoices$/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^overview$/i })).not.toBeInTheDocument();
  });

  /**
   * The decision this screen used to make FOR the user. It auto-picked the
   * cheapest plan and bought it on one tap; the user saw no alternative and,
   * originally, no price. A phone is a smaller screen, not a reason to remove
   * the choice — so every plan the catalog sells is offered, by name and
   * price, and nothing is bought until one is picked.
   */
  it("offers every plan the catalog sells, not just the cheapest", () => {
    renderScreen();

    expect(screen.getByRole("button", { name: /alpha/i })).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /beta/i })).toBeInTheDocument();
  });

  it("shows each plan's own price from the server", () => {
    renderScreen();

    expect(
      screen.getByRole("button", { name: /alpha.*\$23\.00/i }),
    ).toBeInTheDocument();
    expect(
      screen.getByRole("button", { name: /beta.*\$47\.00/i }),
    ).toBeInTheDocument();
  });

  it("orders plans by the server's display_order, not catalog order", () => {
    // The fixture deliberately lists Beta first. Ordering is a server fact.
    renderScreen();
    const names = screen
      .getAllByRole("button")
      .map((b) => b.textContent ?? "")
      .filter((t) => /alpha|beta/i.test(t));
    expect(names[0]).toMatch(/alpha/i);
    expect(names[1]).toMatch(/beta/i);
  });

  it("buys nothing until a plan is chosen", () => {
    renderScreen();
    expect(screen.queryByTestId("embedded-checkout")).not.toBeInTheDocument();
    expect(mutate).not.toHaveBeenCalled();
  });

  it("opens embedded checkout in place for the plan the user picked", async () => {
    const { default: userEvent } = await import("@testing-library/user-event");
    renderScreen();

    await userEvent.setup().click(screen.getByRole("button", { name: /beta/i }));

    // Beta, not the cheapest — the point of offering a choice is that the
    // choice is honoured. A screen that still auto-picked would mount Alpha.
    expect(screen.getByTestId("embedded-checkout")).toHaveAttribute(
      "data-plan-id",
      "tier_beta",
    );
  });

  it("does not send the user to a hosted Stripe URL", async () => {
    // The behaviour this screen was built to stop. A phone is the worst place
    // for a redirect round trip, and returning to a backgrounded tab is where
    // purchases went missing.
    const { default: userEvent } = await import("@testing-library/user-event");
    const before = window.location.href;
    renderScreen();

    await userEvent.setup().click(screen.getByRole("button", { name: /beta/i }));

    expect(window.location.href).toBe(before);
    expect(mutate).not.toHaveBeenCalled();
  });

  it("lets the user back out of a purchase without buying", async () => {
    const { default: userEvent } = await import("@testing-library/user-event");
    const user = userEvent.setup();
    renderScreen();

    await user.click(screen.getByRole("button", { name: /beta/i }));
    expect(screen.getByTestId("embedded-checkout")).toBeInTheDocument();

    await user.click(screen.getByRole("button", { name: /^cancel$/i }));
    expect(screen.queryByTestId("embedded-checkout")).not.toBeInTheDocument();
  });

  it("points users to desktop for invoices, usage charts, and plan comparisons", () => {
    renderScreen();
    expect(
      screen.getByText(/invoices, usage charts, and plan comparisons/i),
    ).toBeInTheDocument();
  });

  it("calls onBack from the header", async () => {
    const { default: userEvent } = await import("@testing-library/user-event");
    const onBack = vi.fn();
    renderScreen(onBack);
    await userEvent.setup().click(
      screen.getByRole("button", { name: /back to settings/i }),
    );
    expect(onBack).toHaveBeenCalled();
  });
});
