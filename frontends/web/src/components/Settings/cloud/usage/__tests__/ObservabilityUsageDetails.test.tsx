import { create } from "@bufbuild/protobuf";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { afterEach, describe, expect, it, vi } from "vitest";
import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import { Code, ConnectError } from "@connectrpc/connect";
import { GetObservabilityLimitsResponseSchema, GetObservabilityStorageResponseSchema, ListObservabilityUsageResponseSchema } from "@/gen/controlplane/services/observability_billing/v1/observability_billing_pb";
import { ObservabilityUsageDetails } from "../ObservabilityUsageDetails";
// Every read runs on its generated hook, on the app transport: the billing reads, the limit
// history (BillingService.ListOrgLimitChanges) and the collection policy (DeployService.ListEnvironments).
function serve(list: (req: { pageToken?: string }) => Promise<unknown>) {
  setTransport(mockTransport({
    "ObservabilityBillingService.GetObservabilityLimits": async () => create(GetObservabilityLimitsResponseSchema),
    "ObservabilityBillingService.GetObservabilityStorage": async () => create(GetObservabilityStorageResponseSchema),
    "ObservabilityBillingService.ListObservabilityUsage": (req) => list(req as { pageToken?: string }),
    "BillingService.ListOrgLimitChanges": { response: { changes: [], nextPageToken: "" } },
    "DeployService.ListEnvironments": { response: { environments: [] } },
  }));
}
afterEach(() => setTransport(null));

describe("estimated split", () => {
  it("says the daily per-service split is estimated only for a billing-exempt org", async () => {
    const list = vi.fn(async () => create(ListObservabilityUsageResponseSchema, { buckets: [{ signal: "logs", service: "api", billedBytes: 1n }], nextPageToken: "" }));
    serve(list);
    const view = (estimatedSplit: boolean) => <QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false } } })}><ObservabilityUsageDetails orgId="org" estimatedSplit={estimatedSplit} /></QueryClientProvider>;
    const { unmount } = render(view(false));
    await screen.findByText("api");
    expect(screen.queryByTestId("obs-daily-estimated")).not.toBeInTheDocument();
    unmount();
    render(view(true));
    expect(await screen.findByTestId("obs-daily-estimated")).toHaveTextContent(/weighted by row count/);
  });
});

describe("itemized history paging", () => {
  it("passes daily service grouping, keeps CSV disabled until all pages arrive, and changes month", async () => {
    const list = vi.fn(async ({ pageToken }: { pageToken?: string }) => create(ListObservabilityUsageResponseSchema, { buckets: [{ signal: "logs", service: pageToken ? "worker" : "api", billedBytes: 1073741824n }], nextPageToken: pageToken ? "" : "next" }));
    serve(list);
    const queryClient = new QueryClient({ defaultOptions: { queries: { retry: false } } });
    render(<QueryClientProvider client={queryClient}><ObservabilityUsageDetails orgId="org" /></QueryClientProvider>);
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
    serve(list);
    render(<QueryClientProvider client={new QueryClient({ defaultOptions: { queries: { retry: false, retryDelay: 0 } } })}><ObservabilityUsageDetails orgId="org" /></QueryClientProvider>);
    await screen.findByText("api"); fireEvent.click(screen.getByRole("button", { name: /load more usage/i }));
    expect(await screen.findByText(/Daily ingestion is unavailable/)).toBeInTheDocument();
    expect(screen.getByRole("button", { name: /download csv/i })).toBeDisabled();
  });
});
