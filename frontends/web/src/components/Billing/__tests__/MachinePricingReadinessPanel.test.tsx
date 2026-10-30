import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { beforeEach, expect, it, vi } from "vitest";
import { MachinePricingReadinessPanel } from "../MachinePricingReadinessPanel";
const mock = vi.hoisted(() => ({ read: vi.fn(), accept: vi.fn(), active: { id: "org-a", role: "owner" } }));
vi.mock("@/hooks/useOrgs", () => ({ useActiveOrg: () => ({ active: mock.active }) }));
vi.mock("@/services/controlPlane/client", () => ({ getControlPlaneClient: () => ({ getMachinePricingReadiness: mock.read, acceptNextMachinePricing: mock.accept }) }));
beforeEach(() => {
  vi.clearAllMocks(); mock.active = { id: "org-a", role: "owner" };
  mock.read.mockResolvedValue({ pricingVersion: "discovered-version", activationReady: true, holdReasons: [], termsSha256: "digest-a", monthlyPriceCents: 6900n, includedOwnSizeHours: 160n, includedDiskGib: 60n, size: "small", exactHourlyRateCents: "45/1", exactCostWeight: "1/1", routeId: "route-a", activationState: "ready", effectivePeriodStart: { seconds: 1900000000n } });
  mock.accept.mockResolvedValue({ activationState: "pending", acceptanceId: "accepted" });
});
function mount() {
  const client = new QueryClient({ defaultOptions: { queries: { retry: false } } });
  const view = () => <QueryClientProvider client={client}><MachinePricingReadinessPanel planId="plan-small" currentPriceLabel="$20" /></QueryClientProvider>;
  return { ...render(view()), view };
}
it("discovers candidate read-only and submits exact org/plan/version/digest, never optimistic activation", async () => {
  mount();
  fireEvent.click(await screen.findByRole("checkbox"));
  expect(mock.read).toHaveBeenCalledWith({ orgId: "org-a", planId: "plan-small", pricingVersion: "" });
  fireEvent.click(screen.getByRole("button", { name: /Accept next-period/ }));
  await waitFor(() => expect(mock.accept).toHaveBeenCalledWith({ orgId: "org-a", planId: "plan-small", pricingVersion: "discovered-version", termsSha256: "digest-a" }));
  expect(await screen.findByText(/Pending verification — not activated/)).toBeInTheDocument();
});
it("clears unchecked/checked consent on org switch and never accepts for a member", async () => {
  const { rerender, view } = mount(); fireEvent.click(await screen.findByRole("checkbox"));
  mock.active = { id: "org-b", role: "member" }; rerender(view());
  await waitFor(() => expect(mock.read).toHaveBeenCalledWith({ orgId: "org-b", planId: "plan-small", pricingVersion: "" }));
  await screen.findByText(/Only this organization’s owner/);
  expect(screen.queryByRole("checkbox")).not.toBeInTheDocument(); expect(mock.accept).not.toHaveBeenCalled();
});
it("missing RPC fails closed with actionable unavailable copy", async () => {
  mock.read.mockRejectedValue(new Error("unimplemented")); mount();
  expect(await screen.findByRole("alert")).toHaveTextContent(/unavailable on this server/);
  expect(screen.queryByRole("checkbox")).not.toBeInTheDocument();
});
