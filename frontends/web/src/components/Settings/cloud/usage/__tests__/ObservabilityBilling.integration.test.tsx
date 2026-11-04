import { create } from "@bufbuild/protobuf";
import { Code, ConnectError } from "@connectrpc/connect";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { UsageView } from "@/components/Forge/Observability/UsageView";
import { ObservabilityBillingApiContext } from "@/hooks/observability-billing-queries";
import { GetObservabilityUsageResponseSchema, GetObservabilityLimitsResponseSchema, GetObservabilityStorageResponseSchema, ListObservabilityUsageResponseSchema } from "@/gen/controlplane/services/observability_billing/v1/observability_billing_pb";

const legacy = vi.hoisted(() => vi.fn(() => ({ isLoading: false, data: null, error: new Error("Legacy read failed"), refetch: vi.fn() })));
vi.mock("@/hooks/observability-queries", () => ({ useObservabilityUsage: legacy }));
vi.mock("@/services/controlPlane/client", () => ({ getControlPlaneClient: () => ({ listOrgLimitChanges: async () => ({ changes: [], nextPageToken: "" }), listEnvironments: async () => ({ environments: [] }) }) }));
function renderUsage(failure?: Code) {
  const get = vi.fn(async () => { if (failure) throw new ConnectError("billing read", failure); return create(GetObservabilityUsageResponseSchema, { usedBytes: 20n * 1073741824n, allowanceBytes: 10n * 1073741824n, chargedCents: 250n, unchargedBytes: 1073741824n, spendCapCents: 5000n, rateCentsPerGb: "25", state: 1 }); });
  const api = { getObservabilityUsage: get, getObservabilityLimits: async () => create(GetObservabilityLimitsResponseSchema), getObservabilityStorage: async () => create(GetObservabilityStorageResponseSchema), listObservabilityUsage: async () => create(ListObservabilityUsageResponseSchema), updateObservabilityLimits: vi.fn() };
  const cache = new QueryClient({ defaultOptions: { queries: { retry: false }, mutations: { retry: false } } });
  render(<QueryClientProvider client={cache}><ObservabilityBillingApiContext.Provider value={api}><UsageView orgId="org-a" /></ObservabilityBillingApiContext.Provider></QueryClientProvider>);
  return get;
}
describe("shared billing view", () => {
  it("uses the billing API and separates actual charges, free bytes and storage", async () => {
    legacy.mockClear(); const get = renderUsage();
    expect(await screen.findByTestId("obs-billing-charged")).toHaveTextContent("$2.50 of $50.00");
    expect(screen.getByTestId("obs-billing-uncharged")).toHaveTextContent("1 GB");
    expect(await screen.findByText(/No stored-now snapshot available/)).toBeInTheDocument();
    expect(get).toHaveBeenCalledWith({ orgId: "org-a" });
    expect(legacy).not.toHaveBeenCalled();
  });
  it("undeployed endpoint is explicit and legacy fallback is read-only", async () => {
    renderUsage(Code.Unimplemented);
    expect(await screen.findByText(/not available on this server yet/)).toBeInTheDocument();
    expect(screen.getByText(/Showing legacy usage below, read-only/)).toBeInTheDocument();
    expect(screen.queryByTestId("obs-billing-charged")).not.toBeInTheDocument();
  });
  it("does not fall back on permission denial or turn it into zero", async () => {
    legacy.mockClear(); renderUsage(Code.PermissionDenied);
    expect(await screen.findByText(/Observability billing is unavailable/)).toBeInTheDocument();
    expect(legacy).not.toHaveBeenCalled();
    expect(screen.queryByTestId("obs-billing-charged")).not.toBeInTheDocument();
  });
});
