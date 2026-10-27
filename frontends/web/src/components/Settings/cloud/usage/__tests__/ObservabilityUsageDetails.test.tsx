import { create } from "@bufbuild/protobuf";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { Code, ConnectError } from "@connectrpc/connect";
import { GetObservabilityLimitsResponseSchema, GetObservabilityStorageResponseSchema, ListObservabilityUsageResponseSchema } from "@/gen/controlplane/services/observability_billing/v1/observability_billing_pb";
import { ObservabilityBillingApiContext } from "@/hooks/observability-billing-queries";
import { ObservabilityUsageDetails } from "../ObservabilityUsageDetails";
vi.mock("@/services/controlPlane/client", () => ({ getControlPlaneClient: () => ({ listOrgLimitChanges: async () => ({ changes: [], nextPageToken: "" }), listEnvironments: async () => ({ environments: [] }) }) }));

describe("itemized history paging", () => {
  it("passes daily service grouping, keeps CSV disabled until all pages arrive, and changes month", async () => {
    const list = vi.fn(async ({ pageToken }: { pageToken?: string }) => create(ListObservabilityUsageResponseSchema, { buckets: [{ signal: "logs", service: pageToken ? "worker" : "api", billedBytes: 1073741824n }], nextPageToken: pageToken ? "" : "next" }));
    const api = { getObservabilityUsage: vi.fn(), getObservabilityLimits: async () => create(GetObservabilityLimitsResponseSchema), getObservabilityStorage: async () => create(GetObservabilityStorageResponseSchema), listObservabilityUsage: list, updateObservabilityLimits: vi.fn() };
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(<QueryClientProvider client={queryClient}><ObservabilityBillingApiContext.Provider value={api}><ObservabilityUsageDetails orgId="org" /></ObservabilityBillingApiContext.Provider></QueryClientProvider>);
    await screen.findByText("api");
    expect(list).toHaveBeenCalledWith(expect.objectContaining({ orgId: "org", granularity: 2, groupBy: 2, pageToken: "" }));
    expect(screen.getByRole("button", { name: /download csv/i })).toBeDisabled();
    fireEvent.click(screen.getByRole("button", { name: /load more usage/i }));
    await screen.findByText("worker");
    await waitFor(() => expect(screen.getByRole("button", { name: /download csv/i })).toBeEnabled());
    expect(list).toHaveBeenLastCalledWith(expect.objectContaining({ pageToken: "next" }));
    fireEvent.change(screen.getByLabelText("Usage month"), { target: { value: "2026-09" } });
    await waitFor(() => expect(list).toHaveBeenLastCalledWith(expect.objectContaining({ start: expect.objectContaining({ seconds: 1788220800n }), end: expect.objectContaining({ seconds: 1790812800n }), pageToken: "" })));
  });
  it("retains partial data as unavailable and never enables a partial CSV after page failure", async () => {
    const list = vi.fn(async ({ pageToken }: { pageToken?: string }) => { if (pageToken) throw new ConnectError("offline", Code.Unavailable); return create(ListObservabilityUsageResponseSchema, { buckets: [{ signal: "logs", service: "api" }], nextPageToken: "next" }); });
    const api = { getObservabilityUsage: vi.fn(), getObservabilityLimits: async () => create(GetObservabilityLimitsResponseSchema), getObservabilityStorage: async () => create(GetObservabilityStorageResponseSchema), listObservabilityUsage: list, updateObservabilityLimits: vi.fn() };
    render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false, retryDelay: 0 } } })}><ObservabilityBillingApiContext.Provider value={api}><ObservabilityUsageDetails orgId="org" /></ObservabilityBillingApiContext.Provider></QueryClientProvider>);
    await screen.findByText("api"); fireEvent.click(screen.getByRole("button", { name: /load more usage/i }));
    expect(await screen.findByText(/Daily ingestion is unavailable/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /download csv/i })).toBeDisabled();
  });
});
