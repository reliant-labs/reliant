/**
 * The canvas's "+" controls and selection bar. React Flow's store is mocked
 * at its hooks (nodes, edges, measured handle bounds), because jsdom cannot
 * lay a canvas out; what is under test is which controls appear, what they
 * are called, and which insert or connect each one asks for.
 */

import { beforeEach, describe, expect, it, vi } from "vitest";
import { fireEvent, render, screen, within } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Edge, Node } from "@xyflow/react";
import type { ReactNode } from "react";

const flow = vi.hoisted(() => ({
  nodes: [] as Node[],
  edges: [] as Edge[],
  handles: {} as Record<string, Array<{ id: string | null; position: string; x: number; y: number; width: number; height: number }>>,
}));

vi.mock("@xyflow/react", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@xyflow/react")>()),
  useNodes: () => flow.nodes,
  useEdges: () => flow.edges,
  useInternalNode: (id: string) => {
    const node = flow.nodes.find((n) => n.id === id);
    return node && { internals: { positionAbsolute: node.position, handleBounds: { source: flow.handles[id] ?? [], target: [] } } };
  },
  ViewportPortal: ({ children }: { children: ReactNode }) => <div data-testid="viewport-portal">{children}</div>,
  EdgeLabelRenderer: ({ children }: { children: ReactNode }) => <div data-testid="edge-labels">{children}</div>,
  BaseEdge: () => <path data-testid="edge-path" />,
  Panel: ({ children }: { children: ReactNode }) => <div data-testid="panel">{children}</div>,
}));

const shortcutHandlers = vi.hoisted(() => ({ current: {} as Record<string, (() => void) | undefined> }));
vi.mock("../../hooks/useWorkflowBuilderShortcuts", () => ({
  useWorkflowBuilderShortcuts: (handlers: Record<string, (() => void) | undefined>) => {
    shortcutHandlers.current = handlers;
  },
  useBuilderShortcutLabel: (id: string) => (id === "connectSelectedSteps" ? "⌘⇧L" : "⌘I"),
}));

const toast = vi.hoisted(() => ({ success: vi.fn(), error: vi.fn() }));
vi.mock("sonner", () => ({ toast }));

import { CustomEdge } from "../../edges/CustomEdge";
import { CanvasInsertProvider, type CanvasInsertApi } from "../CanvasInsertContext";
import { NodeOutputAddButtons } from "../NodeOutputAddButtons";
import { SelectionActions } from "../SelectionActions";

const start: Node = { id: "workflow", type: "eventNode", position: { x: 0, y: 0 }, data: { eventType: "started" } };
const think: Node = { id: "think", type: "actionNode", position: { x: 300, y: 0 }, data: { label: "think", step: { id: "think", type: "call_llm" } } };
const act: Node = { id: "act", type: "actionNode", position: { x: 600, y: 0 }, data: { label: "act", step: { id: "act", type: "run" } } };
const route: Node = {
  id: "route",
  type: "switchNode",
  position: { x: 900, y: 0 },
  data: { cases: [{ id: "yes", condition: "nodes.act.ok", label: "ok" }, { id: "no", condition: "" }] },
};
const right = (id: string | null, y = 40) => ({ id, position: "right", x: 200, y, width: 12, height: 12 });

function api(overrides: Partial<CanvasInsertApi> = {}): CanvasInsertApi {
  return { openPaletteAt: vi.fn(), connect: vi.fn(() => true), ...overrides };
}

function withApi(value: CanvasInsertApi | null, ui: ReactNode) {
  return render(<CanvasInsertProvider value={value}>{ui}</CanvasInsertProvider>);
}

beforeEach(() => {
  flow.nodes = [start, think, act, route];
  flow.edges = [
    { id: "e1", source: "workflow", target: "think" },
    { id: "e2", source: "think", target: "act" },
    { id: "e3", source: "act", target: "route" },
    { id: "e4", source: "route", target: "act", sourceHandle: "yes" },
  ];
  flow.handles = { workflow: [right(null)], think: [right(null)], act: [right(null)], route: [right("yes", 60), right("no", 100)] };
  toast.success.mockReset();
  toast.error.mockReset();
  shortcutHandlers.current = {};
});

describe("NodeOutputAddButtons", () => {
  it("puts a + only on outputs that lead nowhere, named for the step and case", async () => {
    const user = userEvent.setup();
    const insert = api();
    withApi(insert, <NodeOutputAddButtons />);

    const buttons = within(screen.getByTestId("viewport-portal")).getAllByRole("button");
    expect(buttons.map((b) => b.getAttribute("aria-label"))).toEqual(["Add a step after Switch (route), case default"]);

    await user.click(buttons[0]!);
    expect(insert.openPaletteAt).toHaveBeenCalledWith({ kind: "after", nodeId: "route", sourceHandle: "no" });
  });

  it("offers the first step on an empty workflow, and sits just right of the handle", () => {
    flow.nodes = [start];
    flow.edges = [];
    flow.handles = { workflow: [right(null, 24)] };
    withApi(api(), <NodeOutputAddButtons />);
    const plus = screen.getByRole("button", { name: "Add a step after the start" });
    // 200 + 12 (handle) + 12 (gap); 24 + 6 (handle middle) − 11 (half the button).
    expect(plus.style.transform).toBe("translate(224px, 19px)");
  });

  it("shows nothing on a read-only canvas", () => {
    withApi(null, <NodeOutputAddButtons />);
    expect(screen.queryByTestId("viewport-portal")).not.toBeInTheDocument();
    expect(screen.queryAllByRole("button")).toHaveLength(0);
  });
});

describe("CustomEdge's insert +", () => {
  const props = {
    id: "e2",
    source: "think",
    target: "act",
    sourceX: 0,
    sourceY: 0,
    targetX: 200,
    targetY: 0,
    sourcePosition: "right",
    targetPosition: "left",
    selected: false,
  } as unknown as Parameters<typeof CustomEdge>[0];

  it("splices a step into the edge, and shows on hover or focus", async () => {
    const user = userEvent.setup();
    const insert = api();
    withApi(insert, <svg><CustomEdge {...props} /></svg>);

    const plus = screen.getByRole("button", { name: "Insert a step between think and act" });
    expect(plus).toHaveClass("opacity-0", "focus-visible:opacity-100");
    fireEvent.mouseEnter(screen.getByTestId("edge-labels").firstElementChild!);
    expect(plus).toHaveClass("opacity-100");

    await user.click(plus);
    expect(insert.openPaletteAt).toHaveBeenCalledWith({ kind: "splice", edgeId: "e2" });
  });

  it("is always visible on a selected edge, beside its label", () => {
    withApi(api(), <svg><CustomEdge {...props} selected data={{ label: "ok" }} /></svg>);
    const labels = screen.getByTestId("edge-labels");
    expect(within(labels).getByText("ok")).toBeInTheDocument();
    expect(within(labels).getByRole("button", { name: /Insert a step/ })).toHaveClass("opacity-100");
  });

  it("has no + on a read-only canvas", () => {
    withApi(null, <svg><CustomEdge {...props} /></svg>);
    expect(screen.queryByRole("button")).not.toBeInTheDocument();
  });
});

describe("SelectionActions", () => {
  it("offers Add step after the one selected step", async () => {
    const user = userEvent.setup();
    flow.nodes = [start, { ...think, selected: true }, act, route];
    const insert = api();
    withApi(insert, <SelectionActions />);

    const toolbar = screen.getByRole("toolbar", { name: "Selected steps" });
    await user.click(within(toolbar).getByRole("button", { name: /Add step after think/ }));
    expect(insert.openPaletteAt).toHaveBeenCalledWith({ kind: "after", nodeId: "think", sourceHandle: null });
    expect(within(toolbar).getByText("⌘I")).toBeInTheDocument();
  });

  it("connects two selected steps, earlier to later, from the button or the shortcut", async () => {
    const user = userEvent.setup();
    const lone: Node = { id: "lone", type: "actionNode", position: { x: 300, y: 400 }, data: { step: { id: "lone", type: "run" } } };
    flow.nodes = [start, think, { ...act, selected: true }, route, { ...lone, selected: true }];
    const insert = api();
    withApi(insert, <SelectionActions />);

    const connect = screen.getByRole("button", { name: /Connect lone to act/ });
    await user.click(connect);
    expect(insert.connect).toHaveBeenCalledWith("lone", "act");
    expect(toast.success).toHaveBeenCalledWith("Connected lone → act");

    await user.click(screen.getByRole("button", { name: "Swap direction" }));
    expect(screen.getByRole("button", { name: /Connect act to lone/ })).toBeEnabled();
    shortcutHandlers.current.onConnectSelectedSteps?.();
    expect(insert.connect).toHaveBeenLastCalledWith("act", "lone");
  });

  it("says why two steps can't be connected", () => {
    flow.nodes = [start, { ...think, selected: true }, { ...act, selected: true }, route];
    const insert = api();
    withApi(insert, <SelectionActions />);
    expect(screen.getByRole("button", { name: /Connect think to act/ })).toBeDisabled();
    expect(screen.getByText("Already connected")).toBeInTheDocument();
    shortcutHandlers.current.onConnectSelectedSteps?.();
    expect(insert.connect).not.toHaveBeenCalled();
    expect(toast.error).toHaveBeenCalledWith("Already connected");
  });

  it("shows nothing with no selection, or on a read-only canvas", () => {
    const { rerender } = withApi(api(), <SelectionActions />);
    expect(screen.queryByRole("toolbar")).not.toBeInTheDocument();
    flow.nodes = [start, { ...think, selected: true }];
    rerender(<CanvasInsertProvider value={null}><SelectionActions /></CanvasInsertProvider>);
    expect(screen.queryByRole("toolbar")).not.toBeInTheDocument();
    expect(shortcutHandlers.current.onConnectSelectedSteps).toBeUndefined();
  });
});
