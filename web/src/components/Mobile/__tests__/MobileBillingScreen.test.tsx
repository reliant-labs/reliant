/**
 * MobileBillingScreen — simple cards, not the desktop four-tab dashboard.
 * Mocks `useCloudBillingQueries` the same way `billing.test.tsx` does so it
 * renders deterministically with no network.
 */

import { render, screen } from "@testing-library/react";
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
  usePlans: () =>
    query({
      plans: [
        { id: "plan_compute_small", name: "Small", limits: "{}" },
      ],
    }),
  useCreateCheckoutSession: () => mutation(),
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
  it("shows a credit balance card", () => {
    renderScreen();
    expect(screen.getByText("Credit balance")).toBeInTheDocument();
  });

  it("shows a compute plan card with no compute plan by default", () => {
    renderScreen();
    expect(screen.getByText("Compute plan")).toBeInTheDocument();
    expect(screen.getByText("No compute plan")).toBeInTheDocument();
  });

  it("does not render the desktop tab bar or invoices/usage detail tabs", () => {
    renderScreen();
    expect(screen.queryByRole("button", { name: /^invoices$/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^overview$/i })).not.toBeInTheDocument();
  });

  it("offers an Upgrade button that starts checkout for the cheapest plan", async () => {
    const { default: userEvent } = await import("@testing-library/user-event");
    renderScreen();
    const upgrade = screen.getByRole("button", { name: /upgrade/i });
    await userEvent.setup().click(upgrade);
    expect(mutate).toHaveBeenCalledWith(
      expect.objectContaining({ planId: "plan_compute_small" }),
      expect.anything(),
    );
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
