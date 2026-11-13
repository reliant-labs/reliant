/**
 * The builder's side of the tour's canvas channel (useTourCanvas): "Try it
 * yourself" hides an edge without marking the workflow modified, and the edge
 * the user draws back is swapped for the original, so the workflow ends
 * exactly as stored. Leaving without drawing it puts it back. The edge is
 * hidden, never removed, so save, undo and an unmount mid-exercise all keep it.
 */
import { describe, expect, it, vi } from "vitest";
import React, { useState } from "react";
import { act, render } from "@testing-library/react";
import { ReactFlowProvider, type Edge } from "@xyflow/react";

import { tourCanvas } from "../../canvas/tourCanvas";
import { tourEvents } from "../../../Onboarding/tourEvents";
import { useTourCanvas } from "../useTourCanvas";

const ORIGINAL: Edge = { id: "e-review-loop", source: "review", target: "fix_until_green", type: "custom" };
const OTHER: Edge = { id: "e-approve-merge", source: "approve_merge", target: "merge" };

function Harness({
  onState,
  initiallyModified = false,
}: {
  onState: (s: { edges: Edge[]; modified: boolean }) => void;
  initiallyModified?: boolean;
}) {
  const [edges, setEdges] = useState<Edge[]>([ORIGINAL, OTHER]);
  const [modified, setModified] = useState(initiallyModified);
  const wrapperRef = React.useRef<HTMLDivElement>(null);
  useTourCanvas({ wrapperRef, isViewReady: true, setEdges, hasModifications: modified, setHasModifications: setModified });
  onState({ edges, modified });
  return (
    <div ref={wrapperRef}>
      <button
        data-testid="draw"
        onClick={() => {
          // What WorkflowBuilder.createEdge does when the user draws it.
          setEdges((e) => [...e, { id: "e-new", source: "review", target: "fix_until_green" }]);
          setModified(true);
          tourEvents.emit("workflow:edgeConnected", { sourceId: "review", targetId: "fix_until_green" });
        }}
      />
      <button data-testid="edit" onClick={() => setModified(true)} />
    </div>
  );
}

function mount({ initiallyModified = false } = {}) {
  const state = { current: { edges: [] as Edge[], modified: false } };
  const view = render(
    <ReactFlowProvider>
      <Harness initiallyModified={initiallyModified} onState={(s) => (state.current = s)} />
    </ReactFlowProvider>,
  );
  return { state, view };
}

const visible = (edges: Edge[]) => edges.filter((e) => !e.hidden).map((e) => e.id).sort();

describe("useTourCanvas", () => {
  it("detaches the edge unsaved, and puts it back", () => {
    const { state } = mount();
    act(() => tourCanvas.detachEdge("review", "fix_until_green"));
    expect(visible(state.current.edges)).toEqual([OTHER.id]);
    expect(state.current.modified).toBe(false);
    act(() => tourCanvas.reattachEdge());
    expect(state.current.edges).toEqual([ORIGINAL, OTHER]);
    expect(state.current.modified).toBe(false);
  });

  it("keeps the detached edge in the builder's edges, so a save or an unmount mid-exercise cannot lose it", () => {
    const { state, view } = mount();
    act(() => tourCanvas.detachEdge("review", "fix_until_green"));
    // What save serializes (nodesEdgesToWorkflow reads every edge in state).
    expect(state.current.edges).toEqual([{ ...ORIGINAL, hidden: true }, OTHER]);
    view.unmount();
    // The builder is gone mid-exercise: nothing is left detached for a later builder.
    const next = mount();
    act(() => tourCanvas.reattachEdge());
    expect(next.state.current.edges).toEqual([ORIGINAL, OTHER]);
  });

  it.each([
    ["a clean", false],
    ["an already modified", true],
  ])("puts it back on %s workflow and leaves the modified flag as it was", (_label, initiallyModified) => {
    const { state } = mount({ initiallyModified });
    act(() => tourCanvas.detachEdge("review", "fix_until_green"));
    act(() => tourCanvas.reattachEdge());
    expect(state.current.edges).toEqual([ORIGINAL, OTHER]);
    expect(state.current.modified).toBe(initiallyModified);
  });

  it("keeps an edit the user made during the exercise marked, after Put it back", () => {
    const { state, view } = mount();
    act(() => tourCanvas.detachEdge("review", "fix_until_green"));
    act(() => view.getByTestId("edit").click());
    act(() => tourCanvas.reattachEdge());
    expect(state.current.modified).toBe(true);
  });

  it("swaps the edge the user draws for the original, and leaves the workflow unmodified", async () => {
    vi.useFakeTimers();
    const { state, view } = mount();
    act(() => tourCanvas.detachEdge("review", "fix_until_green"));
    act(() => view.getByTestId("draw").click());
    act(() => vi.runAllTimers());
    expect(state.current.edges).toContainEqual(ORIGINAL);
    expect(state.current.edges.find((e) => e.id === "e-new")).toBeUndefined();
    expect(state.current.modified).toBe(false);
    // Nothing left to put back.
    act(() => tourCanvas.reattachEdge());
    expect(state.current.edges.filter((e) => e.id === ORIGINAL.id)).toHaveLength(1);
    vi.useRealTimers();
  });

  it.each([
    ["an already modified workflow", true, false],
    ["an edit made during the exercise", false, true],
  ])("after the draw, keeps %s marked modified", (_label, initiallyModified, editDuring) => {
    vi.useFakeTimers();
    const { state, view } = mount({ initiallyModified });
    act(() => tourCanvas.detachEdge("review", "fix_until_green"));
    if (editDuring) act(() => view.getByTestId("edit").click());
    act(() => view.getByTestId("draw").click());
    act(() => vi.runAllTimers());
    expect(state.current.edges).toEqual([ORIGINAL, OTHER]);
    expect(state.current.modified).toBe(true);
    vi.useRealTimers();
  });
});
