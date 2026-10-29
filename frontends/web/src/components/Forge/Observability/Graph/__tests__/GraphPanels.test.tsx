// Copyright (c) 2026 Reliant Labs
import { fireEvent, render, screen, within } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { fixtureCatalog, fixtureGraph, graphEdge, GRAPH_RPC } from "../../__fixtures__/fixtureGraph";
import { GraphPanel } from "../GraphPanel";
import { catalogNotes, RPCExplorer } from "../RPCExplorer";
vi.mock("../ProcessGraph", () => ({ ProcessGraph: () => <div data-testid="process-graph" /> }));
vi.mock("../../AskReliant/AskReliant", () => ({ AskReliantButton: () => <button>Ask Reliant</button> }));
const scope = { orgId: "org", projectId: "shop", envIds: ["env-prod"], filters: [] };
const envNames = new Map([["env-prod", "prod"]]);
const explorer = (catalog = fixtureCatalog(), patch = {}) => {
  const props = { catalog, service: "", rpcId: "", envNames, onSelect: vi.fn(), onGraph: vi.fn(), onTraces: vi.fn(), ...patch };
  render(<RPCExplorer {...props} />);
  return props;
};

describe("service graph panel", () => {
  it("renders the graph, a compact legend and a connection detail with example traces", () => {
    const navigate = vi.fn();
    render(<GraphPanel graph={fixtureGraph()} filters={{}} scope={scope} navigate={navigate} onRPC={vi.fn()} envNames={envNames} />);
    expect(screen.getByTestId("process-graph")).toBeInTheDocument();
    expect(screen.getByText("Line width = traffic")).toBeInTheDocument();
    fireEvent.click(screen.getByText(/browser → api/));
    const detail = screen.getByRole("region", { name: "Graph detail" });
    expect(detail).toHaveTextContent("browser → api");
    expect(detail).toHaveTextContent("3%");
    fireEvent.click(screen.getByRole("button", { name: "Trace 1" }));
    expect(navigate).toHaveBeenCalledWith(expect.objectContaining({ view: "traces", trace: "a".repeat(32), span: "b".repeat(16) }));
    expect(screen.getByText("Ask Reliant")).toBeInTheDocument();
  });
  it("hides unresolved callers by default and offers to show them", () => {
    const onShow = vi.fn();
    render(<GraphPanel graph={fixtureGraph()} filters={{}} scope={scope} navigate={vi.fn()} onRPC={vi.fn()} envNames={envNames} onShowUnresolved={onShow} />);
    expect(screen.queryByText(/unknown → orders/)).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Show" }));
    expect(onShow).toHaveBeenCalled();
  });
  it("offers to show an unresolved caller when its errors are all a filter matches", () => {
    const onShow = vi.fn();
    const graph = fixtureGraph();
    graph.edges = graph.edges.map((e) => (e.source === "unknown" ? e : { ...e, faults: 0n }));
    render(<GraphPanel graph={graph} filters={{ errorsOnly: true }} scope={scope} navigate={vi.fn()} onRPC={vi.fn()} envNames={envNames} onShowUnresolved={onShow} />);
    expect(screen.queryByTestId("graph-empty")).not.toBeInTheDocument();
    fireEvent.click(screen.getByRole("button", { name: "Show" }));
    expect(onShow).toHaveBeenCalled();
  });
  it("counts a connection's methods from one viewpoint, like its summary", () => {
    const graph = fixtureGraph();
    graph.edges.push(graphEdge("api-orders-client", "api", "orders", { unit: "client_attempt", viewpoint: "client", count: 104n, faults: 5n }));
    render(<GraphPanel graph={graph} filters={{}} scope={scope} navigate={vi.fn()} onRPC={vi.fn()} envNames={envNames} />);
    fireEvent.click(screen.getByText(/api → orders/));
    const detail = screen.getByRole("region", { name: "Graph detail" });
    const row = within(detail).getByRole("button", { name: /GetOrder/ }).closest("tr")!;
    expect(row).toHaveTextContent("100");
    expect(row).not.toHaveTextContent("204");
  });
  it("never prints environment ids or internal reason codes", () => {
    const { container } = render(<GraphPanel graph={fixtureGraph()} filters={{ showUnresolved: true }} scope={scope} navigate={vi.fn()} onRPC={vi.fn()} envNames={envNames} />);
    fireEvent.click(screen.getByText(/api → orders/));
    expect(container.textContent).not.toMatch(/env-prod|PRE_TAIL|unmatched/i);
  });
  it("shows a plain empty state", () => {
    render(<GraphPanel graph={fixtureGraph(true, true)} filters={{}} scope={scope} navigate={vi.fn()} onRPC={vi.fn()} envNames={envNames} />);
    expect(screen.getByTestId("graph-empty")).toHaveTextContent("No traffic in this window");
  });
});

describe("RPC explorer", () => {
  it("lists methods with requests, error rate and latency percentiles", () => {
    const props = explorer();
    const row = screen.getByTestId(`rpc-row-${GRAPH_RPC}`);
    expect(row).toHaveTextContent("GetOrder");
    expect(row).toHaveTextContent("100");
    expect(row).toHaveTextContent("3%");
    expect(row).toHaveTextContent("8.33ms"); // p50: rank 50 of the 60 in the 0–10ms bucket
    expect(row).toHaveTextContent("161.1ms"); // p95: rank 95, 5 of 9 into the 50–250ms bucket
    fireEvent.click(screen.getByTitle(GRAPH_RPC));
    expect(props.onSelect).toHaveBeenCalledWith(GRAPH_RPC);
    fireEvent.click(screen.getByRole("button", { name: `Traces for ${GRAPH_RPC}` }));
    expect(props.onTraces).toHaveBeenCalledWith(GRAPH_RPC);
  });
  it("shows declared request/response schemas for the selected method", () => {
    const props = explorer(fixtureCatalog(), { rpcId: GRAPH_RPC });
    const detail = screen.getByRole("region", { name: "RPC method detail" });
    expect(detail).toHaveTextContent("GetOrder");
    expect(detail).toHaveTextContent("shop.v1.GetOrderRequest");
    expect(screen.getAllByText(/"validate_options":/).length).toBeGreaterThanOrEqual(1);
    fireEvent.click(screen.getByRole("button", { name: "Show in graph" }));
    expect(props.onGraph).toHaveBeenCalledWith(GRAPH_RPC);
    expect(screen.queryByRole("button", { name: /execute|send request/i })).not.toBeInTheDocument();
  });
  it("an unavailable catalog is not a wall of caveats", () => {
    const { container } = render(<RPCExplorer catalog={fixtureCatalog(false, true)} service="" rpcId={GRAPH_RPC} envNames={envNames} onSelect={vi.fn()} onGraph={vi.fn()} onTraces={vi.fn()} />);
    expect(container.textContent).not.toMatch(/Browse only|Declared metadata unavailable|does not establish|catalog_target_unavailable|env-prod/);
    expect(screen.getByRole("region", { name: "RPC method detail" })).toBeInTheDocument();
    expect(catalogNotes(fixtureCatalog(false, true), envNames)).toEqual(["Request and response schemas: No verified hosted artifact catalog"]);
  });
  it("shows a plain empty state", () => {
    explorer(fixtureCatalog(true));
    expect(screen.getByText("No RPC traffic in this window")).toBeInTheDocument();
  });
  it("keeps every artifact's schema, labelled by environment name", () => {
    const c = fixtureCatalog();
    c.declaredCatalogs = Array.from({ length: 21 }, (_, i) => ({ ...c.declaredCatalogs[0], envId: `env-${i}`, bundleDigest: `bundle-${i}` }));
    const last = JSON.parse(c.declaredCatalogs[20].json);
    last.services[0].descriptor.Schemas["shop.v1.GetOrderRequest"] = [{ name: "different_schema_in_21st", kind: "int64" }];
    c.declaredCatalogs[20].json = JSON.stringify(last);
    explorer(c, { rpcId: GRAPH_RPC, envNames: new Map([["env-20", "staging"]]) });
    expect(screen.getAllByText(/different_schema_in_21st/).length).toBeGreaterThan(0);
    expect(screen.getByText(/^staging ·/)).toBeInTheDocument();
  });
  it("reports malformed metadata in the info notes without crashing", () => {
    const c = fixtureCatalog(); c.declaredCatalogs[0].json = "{bad";
    explorer(c, { rpcId: GRAPH_RPC });
    expect(screen.getByTestId("rpc-explorer")).toBeInTheDocument();
    expect(catalogNotes(c, envNames)).toEqual(["The service catalog for prod could not be read (catalog json is invalid)."]);
  });
});
