/**
 * The workflows journey's emit sites: the real interaction points send the
 * signals its steps wait on (journeys.workflows.test.tsx covers signal →
 * advance). The builder's own callbacks (createEdge, onNodeClick,
 * declareTrigger) emit inline; these cover the extracted pieces.
 */
import { afterEach, describe, expect, it, vi } from "vitest";
import React from "react";
import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import type { Edge, Node } from "@xyflow/react";

const seedFixMyPr = vi.hoisted(() => vi.fn());
vi.mock("../journeys/fixMyPr", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../journeys/fixMyPr")>()),
  seedFixMyPr,
}));
const createDraftMutate = vi.hoisted(() => vi.fn());
vi.mock("@/hooks/workflow-library-queries", () => ({
  useCreateWorkflowDraft: () => ({ mutate: createDraftMutate, isPending: false }),
}));
vi.mock("../../Automations/dialog/AutomationDialog", () => ({ AutomationDialog: () => null }));

import { tourEvents } from "../tourEvents";
import type { TourEventName } from "../tourEvents";
import { describeInsert, stepTypeOfNode, type InsertAnchor } from "../../workflow/canvas/insertPlacement";
import { NewWorkflowDialog } from "../../workflow/NewWorkflowDialog";
import { AutomationDialogHost } from "../../Automations/dialog/AutomationDialogHost";
import { openAutomationDialog, useAutomationDialogStore } from "../../Automations/dialog/openAutomationDialog";

function record(...names: TourEventName[]) {
  const seen: [string, unknown][] = [];
  const offs = names.map((name) => tourEvents.on(name, (payload) => seen.push([name, payload])));
  return { seen, off: () => offs.forEach((off) => off()) };
}

afterEach(() => {
  vi.clearAllMocks();
});

describe("workflow:nodeAdded's anchor (CanvasInsertContext.insertNode)", () => {
  const node = (id: string, extra: Partial<Node> = {}): Node => ({ id, position: { x: 0, y: 0 }, data: {}, ...extra });
  const nodes = [node("switch_1", { type: "switchNode" }), node("merge"), node("report")];
  const edges: Edge[] = [{ id: "e1", source: "switch_1", target: "merge" }];

  it("a + on an edge is a splice, naming the node the edge led into", () => {
    const pending: InsertAnchor = { kind: "splice", edgeId: "e1" };
    expect(describeInsert(pending, pending, { nodes, edges, selectedNodeId: null })).toEqual({
      anchor: "splice",
      spliceTarget: "merge",
    });
  });

  it("a + on an output, the selection, or the end of the path", () => {
    const output: InsertAnchor = { kind: "after", nodeId: "merge" };
    expect(describeInsert(output, output, { nodes, edges, selectedNodeId: null })).toEqual({ anchor: "output" });
    const after: InsertAnchor = { kind: "after", nodeId: "merge" };
    expect(describeInsert(after, null, { nodes, edges, selectedNodeId: "merge" })).toEqual({ anchor: "selection" });
    expect(describeInsert(after, null, { nodes, edges, selectedNodeId: null })).toEqual({ anchor: "end" });
    expect(describeInsert(null, null, { nodes: [], edges: [], selectedNodeId: null })).toEqual({ anchor: "end" });
  });

  it("names the step type, or switch for a canvas Switch", () => {
    expect(stepTypeOfNode(node("a", { type: "actionNode", data: { step: { type: "approval" } } }))).toBe("approval");
    expect(stepTypeOfNode(node("l", { type: "loopNode", data: { step: { type: "loop" } } }))).toBe("loop");
    expect(stepTypeOfNode(node("s", { type: "switchNode" }))).toBe("switch");
  });
});

describe("NewWorkflowDialog's guided start (W1)", () => {
  it("is offered only during the tour, preselected and prefilled, and seeds Fix my PR", async () => {
    seedFixMyPr.mockResolvedValue({ slug: "fix-my-pr", draftId: "d1", title: "Fix my PR" });
    const onCreated = vi.fn();
    render(React.createElement(NewWorkflowDialog, { open: true, projectId: "p1", onClose: vi.fn(), onCreated, guided: true }));
    const guided = await screen.findByRole("radio", { name: /fix my pr \(guided\)/i });
    expect(guided).toBeChecked();
    expect(screen.getByLabelText("Name")).toHaveValue("Fix my PR");
    expect(document.querySelector("[data-tour='new-workflow-dialog']")).not.toBeNull();

    await userEvent.click(screen.getByRole("button", { name: /create workflow/i }));
    expect(seedFixMyPr).toHaveBeenCalledWith("p1", "Fix my PR");
    expect(createDraftMutate).not.toHaveBeenCalled();
    await vi.waitFor(() => expect(onCreated).toHaveBeenCalledWith({ slug: "fix-my-pr", title: "Fix my PR", draftId: "d1" }));
  });

  it("is absent outside the tour", () => {
    render(React.createElement(NewWorkflowDialog, { open: true, projectId: "p1", onClose: vi.fn(), onCreated: vi.fn() }));
    expect(screen.queryByRole("radio", { name: /guided/i })).toBeNull();
    expect(screen.getByLabelText("Name")).toHaveValue("");
  });
});

describe("the automation dialog (W6)", () => {
  it("emits automation:dialogOpened on open and automation:dialogClosed on close", () => {
    const rec = record("automation:dialogOpened", "automation:dialogClosed");
    render(React.createElement(AutomationDialogHost));
    act(() => openAutomationDialog({ workflowRef: "fix-my-pr", declaredTrigger: "schedule", projectId: "p1" }));
    expect(rec.seen).toEqual([["automation:dialogOpened", { workflowRef: "fix-my-pr", declaredTrigger: "schedule" }]]);
    act(() => useAutomationDialogStore.getState().close());
    expect(rec.seen.at(-1)).toEqual(["automation:dialogClosed", { workflowRef: "fix-my-pr", declaredTrigger: "schedule" }]);
    rec.off();
  });
});
