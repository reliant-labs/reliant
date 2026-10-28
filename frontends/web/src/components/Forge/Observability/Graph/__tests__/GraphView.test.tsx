// Copyright (c) 2026 Reliant Labs
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { QueryClient, QueryClientProvider } from "@tanstack/react-query";
import { Code, ConnectError } from "@connectrpc/connect";
import { describe, expect, it, vi } from "vitest";
import { fixtureCatalog, fixtureGraph } from "../../__fixtures__/fixtureGraph";
import { GraphView } from "../GraphView";
const api = { getServiceGraph: vi.fn(), getServiceGraphCatalog: vi.fn() };
vi.mock("@/hooks/observability-queries", () => ({ obsKeys: { all: (s: unknown) => ["observability", s] }, useObsReadApi: () => api }));
vi.mock("../GraphPanel", () => ({ GraphPanel: () => <div>Graph populated</div> }));
const scope = { orgId: "org", projectId: "shop", envIds: ["env-prod"], filters: [] };
function show(search = {}) {
  const navigate = vi.fn();
  const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
  render(<QueryClientProvider client={client}><GraphView scope={scope} range="15m" search={{ view: "graph", ...search }} navigate={navigate} subject="shop" /></QueryClientProvider>);
  return navigate;
}
describe("scoped graph boundary", () => {
  it("sends full service filter without enumerating/truncating methods", async () => {
    api.getServiceGraph.mockResolvedValue(fixtureGraph()); api.getServiceGraphCatalog.mockResolvedValue(fixtureCatalog());
    const navigate = show({ rpcService: "shop.v1.OrdersService" });
    await waitFor(() => expect(api.getServiceGraph).toHaveBeenCalledWith(expect.objectContaining({ ...scope, rpcIds: [], rpcService: "shop.v1.OrdersService" })));
    expect(await screen.findByText("Graph populated")).toBeInTheDocument();
    fireEvent.click(screen.getByLabelText("Declared overlay"));
    expect(navigate).toHaveBeenCalledWith({ declared: true });
  });
  it("renders backend failure with retry, not empty graph success", async () => {
    api.getServiceGraph.mockRejectedValue(new ConnectError("denied", Code.PermissionDenied));
    api.getServiceGraphCatalog.mockResolvedValue(fixtureCatalog());
    show();
    expect(await screen.findByTestId("obs-error-denied")).toBeInTheDocument();
    expect(screen.queryByText("Graph populated")).not.toBeInTheDocument();
  });
  it("does not query or expose wider declared catalog for an unsupported process filter", async () => {
    api.getServiceGraphCatalog.mockClear();
    show({ view: "rpc", service: "api" });
    expect(screen.getByText(/RPC catalog and declared process metadata are unsupported/)).toBeInTheDocument();
    expect(api.getServiceGraphCatalog).not.toHaveBeenCalled();
    expect(screen.queryByTestId("rpc-explorer")).not.toBeInTheDocument();
  });
  it("does not request a declared overlay under an unmapped process restriction", async () => {
    api.getServiceGraph.mockClear().mockResolvedValue(fixtureGraph());
    show({ service: "api", declared: true });
    await waitFor(() => expect(api.getServiceGraph).toHaveBeenCalledWith(expect.objectContaining({ services: ["api"], includeDeclared: false })));
    expect(screen.getByLabelText("Declared overlay")).toBeDisabled();
  });
  it("looks up selected RPC 201 independently of the 200-method enumeration", async () => {
    const base = fixtureCatalog();
    const selectedId = "shop.v1.OrdersService/ZZMethod201";
    const exact = { ...base, methods: [{ ...base.methods[0], id: selectedId, method: "ZZMethod201" }] };
    api.getServiceGraphCatalog.mockImplementation((req) => Promise.resolve(req.rpcIds?.length ? exact : { ...base, methods: [] }));
    show({ view: "rpc", rpcId: selectedId });
    expect(await screen.findByRole("region", { name: "RPC method detail" })).toHaveTextContent(selectedId);
    expect(api.getServiceGraphCatalog).toHaveBeenCalledWith(expect.objectContaining({ rpcIds: [selectedId] }));
    expect(screen.queryByText("Selected RPC is not present in this scoped catalog.")).not.toBeInTheDocument();
  });
  it("recovers the exact selected method after a retained-data authorization failure", async () => {
    const base = fixtureCatalog(); const id = "shop.v1.OrdersService/ZZMethod600";
    let denied = false;
    api.getServiceGraphCatalog.mockImplementation((req) => denied ? Promise.reject(new ConnectError("revoked", Code.PermissionDenied)) : Promise.resolve({ ...base, methods: req.rpcIds?.length ? [{ ...base.methods[0], id, method: "ZZMethod600" }] : [] }));
    const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
    render(<QueryClientProvider client={client}><GraphView scope={scope} range="15m" search={{ view: "rpc", rpcId: id }} navigate={vi.fn()} subject="shop" /></QueryClientProvider>);
    expect(await screen.findByRole("region", { name: "RPC method detail" })).toHaveTextContent(id);
    denied = true;
    await act(async () => { await client.invalidateQueries(); });
    await waitFor(() => expect(screen.queryByTestId("rpc-explorer")).not.toBeInTheDocument());
    denied = false;
    await act(async () => { await client.invalidateQueries(); });
    expect(await screen.findByRole("region", { name: "RPC method detail" })).toHaveTextContent(id);
  });
  it("names an unsupported older server rather than offering generic retry", async () => {
    api.getServiceGraphCatalog.mockRejectedValue(new ConnectError("unknown procedure", Code.Unimplemented));
    show({ view: "rpc" });
    expect(await screen.findByText(/This server does not support/)).toBeInTheDocument();
    expect(screen.queryByText("Try again")).not.toBeInTheDocument();
  });
  it.each([Code.PermissionDenied, Code.Unavailable])("hides retained catalog data after failed React Query refetch (%s)", async (code) => {
    api.getServiceGraphCatalog.mockResolvedValue(fixtureCatalog());
    const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
    render(<QueryClientProvider client={client}><GraphView scope={scope} range="15m" search={{ view: "rpc", rpcId: "shop.v1.OrdersService/GetOrder" }} navigate={vi.fn()} subject="shop" /></QueryClientProvider>);
    expect(await screen.findByText("Input schema / validation")).toBeInTheDocument();
    api.getServiceGraphCatalog.mockRejectedValue(new ConnectError("catalog revoked", code));
    await act(async () => { await client.invalidateQueries(); });
    await waitFor(() => expect(client.getQueryCache().getAll().some((q) => q.state.status === "error" && q.state.data !== undefined)).toBe(true));
    expect(screen.queryByTestId("rpc-explorer")).not.toBeInTheDocument();
    expect(screen.queryByText("Input schema / validation")).not.toBeInTheDocument();
    expect(screen.queryByRole("option", { name: "shop.v1.OrdersService" })).not.toBeInTheDocument();
  });
});
