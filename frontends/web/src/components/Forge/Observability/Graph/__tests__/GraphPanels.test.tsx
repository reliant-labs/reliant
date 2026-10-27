// Copyright (c) 2026 Reliant Labs
import { fireEvent, render, screen } from "@testing-library/react";
import { describe, expect, it, vi } from "vitest";
import { fixtureCatalog, fixtureGraph, GRAPH_RPC } from "../../__fixtures__/fixtureGraph";
import { GraphPanel } from "../GraphPanel";
import { RPCExplorer } from "../RPCExplorer";
vi.mock("../ProcessGraph", () => ({ ProcessGraph: () => <div data-testid="process-graph" /> }));
vi.mock("../../AskReliant/AskReliant", () => ({ AskReliantButton: () => <button>Ask Reliant</button> }));
const scope = { orgId: "org", projectId: "shop", envIds: ["env-prod"], filters: [] };

describe("graph and explorer honest panel states", () => {
  it("renders populated graph, declaration legend and representative trace link", () => {
    const navigate = vi.fn();
    render(<GraphPanel graph={fixtureGraph()} catalog={fixtureCatalog()} filters={{}} scope={scope} navigate={navigate} onRPC={vi.fn()} />);
    expect(screen.getByTestId("process-graph")).toBeInTheDocument();
    expect(screen.getByText("┄ Declared only")).toBeInTheDocument();
    fireEvent.click(screen.getByText(/browser → api/));
    expect(screen.getByText(/Matched: 97/)).toBeInTheDocument();
    fireEvent.click(screen.getByText(/Open span waterfall/));
    expect(navigate).toHaveBeenCalledWith(expect.objectContaining({ view: "traces", trace: "a".repeat(32), span: "b".repeat(16) }));
    expect(screen.getByText(/trace may not have survived/)).toBeInTheDocument();
    expect(screen.getByText("Ask Reliant")).toBeInTheDocument();
  });
  it("shows empty and unknown coverage explicitly", () => {
    render(<GraphPanel graph={fixtureGraph(true, true)} filters={{}} scope={scope} navigate={vi.fn()} onRPC={vi.fn()} />);
    expect(screen.getByTestId("graph-empty")).toBeInTheDocument();
    expect(screen.getByText(/Coverage metadata unavailable/)).toBeInTheDocument();
    expect(screen.getByText(/No evidence does not establish zero traffic/)).toBeInTheDocument();
  });
  it("catalog unavailable is not an empty success or fabricated schemas", () => {
    render(<RPCExplorer catalog={fixtureCatalog(false, true)} service="" rpcId={GRAPH_RPC} onSelect={vi.fn()} onGraph={vi.fn()} />);
    expect(screen.getByText("Declared metadata unavailable")).toBeInTheDocument();
    expect(screen.getByText("No verified hosted artifact catalog")).toBeInTheDocument();
    expect(screen.queryByText("Input schema / validation")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /execute|send request/i })).not.toBeInTheDocument();
  });
  it("renders supplied schemas/validation and capability metadata as browse-only text", () => {
    render(<RPCExplorer catalog={fixtureCatalog()} service="" rpcId={GRAPH_RPC} onSelect={vi.fn()} onGraph={vi.fn()} />);
    expect(screen.getByText("Input schema / validation")).toBeInTheDocument();
    expect(screen.getAllByText(/"validate_options":/).length).toBeGreaterThanOrEqual(1);
    expect(screen.getByText(/Declared serves: Orders API/)).toBeInTheDocument();
    expect(screen.getByText(/Not a per-method execution claim/)).toBeInTheDocument();
    expect(screen.queryByRole("link", { name: /source/i })).not.toBeInTheDocument();
    expect(screen.getByText(/No requests can be prepared or executed/)).toBeInTheDocument();
  });
  it("retains the 21st artifact and exposes disagreements instead of silently dropping it", () => {
    const c = fixtureCatalog();
    c.declaredCatalogs = Array.from({ length: 21 }, (_, i) => ({ ...c.declaredCatalogs[0], envId: `env-${i}`, bundleDigest: `bundle-${i}` }));
    const last = JSON.parse(c.declaredCatalogs[20].json);
    last.services[0].descriptor.Schemas["shop.v1.GetOrderRequest"] = [{ name: "different_schema_in_21st", kind: "int64" }];
    c.declaredCatalogs[20].json = JSON.stringify(last);
    render(<RPCExplorer catalog={c} service="" rpcId={GRAPH_RPC} onSelect={vi.fn()} onGraph={vi.fn()} />);
    expect(screen.getByText("Verified declared artifact · env-20")).toBeInTheDocument();
    expect(screen.getAllByText(/different_schema_in_21st/).length).toBeGreaterThan(0);
  });
  it("rejects malformed verified metadata without crashing catalog", () => {
    const c = fixtureCatalog(); c.declaredCatalogs[0].json = "{bad";
    render(<RPCExplorer catalog={c} service="" rpcId={GRAPH_RPC} onSelect={vi.fn()} onGraph={vi.fn()} />);
    expect(screen.getByText(/Catalog JSON is invalid/)).toBeInTheDocument();
  });
});
