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
function show(search = {}, client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } })) {
  const navigate = vi.fn();
  render(<QueryClientProvider client={client}><GraphView scope={scope} range="15m" search={{ view: "graph", ...search }} navigate={navigate} subject="shop" /></QueryClientProvider>);
  return navigate;
}
describe("scoped graph boundary", () => {
  it("sends the service filter and toggles the declared overlay", async () => {
    api.getServiceGraph.mockResolvedValue(fixtureGraph()); api.getServiceGraphCatalog.mockResolvedValue(fixtureCatalog());
    const navigate = show({ rpcService: "shop.v1.OrdersService" });
    await waitFor(() => expect(api.getServiceGraph).toHaveBeenCalledWith(expect.objectContaining({ ...scope, rpcIds: [], rpcService: "shop.v1.OrdersService" })));
    expect(await screen.findByText("Graph populated")).toBeInTheDocument();
    fireEvent.click(screen.getByRole("switch", { name: "Declared" }));
    expect(navigate).toHaveBeenCalledWith({ declared: true });
  });
  it("populates the service and method dropdowns from observed traffic", async () => {
    api.getServiceGraph.mockResolvedValue(fixtureGraph()); api.getServiceGraphCatalog.mockResolvedValue(fixtureCatalog());
    const navigate = show();
    await waitFor(() => expect(api.getServiceGraphCatalog).toHaveBeenCalledWith(expect.objectContaining({ rpcService: "" })));
    await waitFor(() => expect(screen.getByLabelText("Service")).not.toHaveTextContent(/loading/i));
    fireEvent.click(screen.getByLabelText("Service"));
    fireEvent.click(await screen.findByRole("option", { name: /OrdersService/ }));
    expect(navigate).toHaveBeenCalledWith({ rpcService: "shop.v1.OrdersService", rpcId: undefined });
    fireEvent.click(screen.getByLabelText("Method"));
    fireEvent.click(await screen.findByRole("option", { name: /GetOrder/ }));
    expect(navigate).toHaveBeenCalledWith({ rpcId: "shop.v1.OrdersService/GetOrder", rpcService: "shop.v1.OrdersService" });
  });
  it("renders backend failure with retry, not empty graph success", async () => {
    api.getServiceGraph.mockRejectedValue(new ConnectError("denied", Code.PermissionDenied));
    api.getServiceGraphCatalog.mockResolvedValue(fixtureCatalog());
    show();
    expect(await screen.findByTestId("obs-error-denied")).toBeInTheDocument();
    expect(screen.queryByText("Graph populated")).not.toBeInTheDocument();
  });
  it("does not query or expose a wider catalog for an unsupported process filter", async () => {
    api.getServiceGraphCatalog.mockClear();
    show({ view: "rpc", service: "api" });
    expect(screen.getByText(/Methods can't be narrowed to one process/)).toBeInTheDocument();
    expect(api.getServiceGraphCatalog).not.toHaveBeenCalled();
    expect(screen.queryByTestId("rpc-explorer")).not.toBeInTheDocument();
  });
  it("does not request a declared overlay under an unmapped process restriction", async () => {
    api.getServiceGraph.mockClear().mockResolvedValue(fixtureGraph());
    show({ service: "api", declared: true });
    await waitFor(() => expect(api.getServiceGraph).toHaveBeenCalledWith(expect.objectContaining({ services: ["api"], includeDeclared: false })));
    expect(screen.getByRole("switch", { name: "Declared" })).toBeDisabled();
  });
  it("looks up selected RPC 201 independently of the method enumeration", async () => {
    const base = fixtureCatalog();
    const selectedId = "shop.v1.OrdersService/ZZMethod201";
    const exact = { ...base, methods: [{ ...base.methods[0], id: selectedId, method: "ZZMethod201" }] };
    api.getServiceGraphCatalog.mockImplementation((req) => Promise.resolve(req.rpcIds?.length ? exact : { ...base, methods: [] }));
    show({ view: "rpc", rpcId: selectedId, rpcService: "shop.v1.OrdersService" });
    expect(await screen.findByRole("region", { name: "RPC method detail" })).toHaveTextContent("ZZMethod201");
    expect(api.getServiceGraphCatalog).toHaveBeenCalledWith(expect.objectContaining({ rpcIds: [selectedId] }));
  });
  it("recovers the exact selected method after a retained-data authorization failure", async () => {
    const base = fixtureCatalog(); const id = "shop.v1.OrdersService/ZZMethod600";
    let denied = false;
    api.getServiceGraphCatalog.mockImplementation((req) => denied ? Promise.reject(new ConnectError("revoked", Code.PermissionDenied)) : Promise.resolve({ ...base, methods: req.rpcIds?.length ? [{ ...base.methods[0], id, method: "ZZMethod600" }] : [] }));
    const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
    show({ view: "rpc", rpcId: id }, client);
    expect(await screen.findByRole("region", { name: "RPC method detail" })).toHaveTextContent("ZZMethod600");
    denied = true;
    await act(async () => { await client.invalidateQueries(); });
    await waitFor(() => expect(screen.queryByTestId("rpc-explorer")).not.toBeInTheDocument());
    denied = false;
    await act(async () => { await client.invalidateQueries(); });
    expect(await screen.findByRole("region", { name: "RPC method detail" })).toHaveTextContent("ZZMethod600");
  });
  it("names an unsupported older server rather than offering generic retry", async () => {
    api.getServiceGraphCatalog.mockRejectedValue(new ConnectError("unknown procedure", Code.Unimplemented));
    show({ view: "rpc" });
    expect(await screen.findByText(/does not support the service graph/)).toBeInTheDocument();
    expect(screen.queryByText("Try again")).not.toBeInTheDocument();
  });
  it.each([Code.PermissionDenied, Code.Unavailable])("hides retained catalog data after failed React Query refetch (%s)", async (code) => {
    api.getServiceGraphCatalog.mockResolvedValue(fixtureCatalog());
    const client = new QueryClient({ defaultOptions: { queries: { retry: false, gcTime: 0 } } });
    show({ view: "rpc", rpcId: "shop.v1.OrdersService/GetOrder" }, client);
    expect(await screen.findByText("shop.v1.GetOrderRequest")).toBeInTheDocument();
    api.getServiceGraphCatalog.mockRejectedValue(new ConnectError("catalog revoked", code));
    await act(async () => { await client.invalidateQueries(); });
    await waitFor(() => expect(client.getQueryCache().getAll().some((q) => q.state.status === "error" && q.state.data !== undefined)).toBe(true));
    expect(screen.queryByTestId("rpc-explorer")).not.toBeInTheDocument();
    expect(screen.queryByText("shop.v1.GetOrderRequest")).not.toBeInTheDocument();
    fireEvent.click(screen.getByLabelText("Service"));
    expect(screen.queryByRole("option", { name: /OrdersService/ })).not.toBeInTheDocument();
  });
});
