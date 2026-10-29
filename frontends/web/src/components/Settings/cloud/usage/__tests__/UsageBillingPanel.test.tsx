import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, expect, it, vi } from "vitest";
import { UsageBillingPanel } from "../UsageBillingPanel";

const mocks = vi.hoisted(() => ({ read: vi.fn(), checkout: vi.fn(), open: vi.fn(), active: { id: "org-a", role: "owner" } }));
vi.mock("@/hooks/useOrgs", () => ({ useActiveOrg: () => ({ active: mocks.active }) }));
vi.mock("@/services/controlPlane/client", () => ({ getControlPlaneClient: () => ({ getCurrentUserUsageBilling: mocks.read, createCurrentUserUsageBillingCheckout: mocks.checkout }) }));
vi.mock("@/lib/stripeCheckout", () => ({ openCheckout: mocks.open, buildCheckoutReturnUrls: () => ({ successUrl: "https://app.test/settings/billing?tab=usage&checkout=success", cancelUrl: "https://app.test/settings/billing?tab=usage&checkout=cancelled" }) }));

beforeEach(() => {
  vi.clearAllMocks();
  mocks.active = { id: "org-a", role: "owner" };
  mocks.read.mockResolvedValue({ enabled: true, policyVersion: "p1", rates: [{ displayName: "CPU", priceCents: "9", unit: "vCPU-hour" }] });
  mocks.checkout.mockResolvedValue({ checkoutUrl: "https://checkout.stripe.test/session" });
  mocks.open.mockResolvedValue("success");
});
function mount() {
  const cache = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  const view = () => <QueryClientProvider client={cache}><UsageBillingPanel /></QueryClientProvider>;
  return { ...render(view()), view };
}
async function consent() {
  fireEvent.change(await screen.findByLabelText(/Monthly app hosting admission budget/), { target: { value: "25.00" } });
  fireEvent.click(screen.getByRole("checkbox"));
  fireEvent.click(screen.getByRole("button", { name: /Authorize and continue/ }));
}
it("submits explicit org/policy/budget and does not treat checkout success as active", async () => {
  mount();
  await screen.findByRole("checkbox");
  expect(mocks.read).toHaveBeenCalledWith({ orgId: "org-a" });
  expect(screen.getByText("CPU: 9 cents / vCPU-hour")).toBeInTheDocument();
  await consent();
  await waitFor(() => expect(mocks.checkout).toHaveBeenCalledWith({ orgId: "org-a", policyVersion: "p1", infraBudgetCents: 2500n, successUrl: "https://app.test/settings/billing?tab=usage#app-hosting", cancelUrl: "https://app.test/settings/billing?tab=usage#app-hosting" }));
  await waitFor(() => expect(mocks.open).toHaveBeenCalled());
  expect(screen.queryByText("App hosting billing is active.")).not.toBeInTheDocument();
});
it("clears consent on org switch and keys the new read to that org", async () => {
  const { rerender, view } = mount();
  fireEvent.click(await screen.findByRole("checkbox"));
  mocks.active = { id: "org-b", role: "owner" };
  rerender(view());
  await waitFor(() => expect(mocks.read).toHaveBeenCalledWith({ orgId: "org-b" }));
  expect(await screen.findByRole("checkbox")).not.toBeChecked();
  expect(mocks.checkout).not.toHaveBeenCalled();
});
it("never opens an old org's checkout after switching during creation", async () => {
  let finish!: (value: { checkoutUrl: string }) => void;
  mocks.checkout.mockImplementation(() => new Promise(resolve => { finish = resolve; }));
  const { rerender, view } = mount();
  await consent();
  await waitFor(() => expect(mocks.checkout).toHaveBeenCalled());
  mocks.active = { id: "org-b", role: "owner" };
  rerender(view());
  await waitFor(() => expect(mocks.read).toHaveBeenCalledWith({ orgId: "org-b" }));
  finish({ checkoutUrl: "https://checkout.stripe.test/old-org" });
  await waitFor(() => expect(screen.getByRole("checkbox")).not.toBeChecked());
  expect(mocks.open).not.toHaveBeenCalled();
});
it("shows an actionable error when checkout returns no URL", async () => {
  mocks.checkout.mockResolvedValue({ checkoutUrl: "" });
  mount();
  await consent();
  expect(await screen.findByRole("alert")).toHaveTextContent(/Checkout could not be completed/);
  expect(mocks.open).not.toHaveBeenCalled();
});
