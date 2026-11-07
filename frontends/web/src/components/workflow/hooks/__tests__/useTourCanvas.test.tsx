/**
 * The builder's side of the tour's canvas channel (useTourCanvas): "Try it
 * yourself" takes an edge out without marking the workflow modified, and the
 * edge the user draws back is swapped for the original, so the workflow ends
 * exactly as stored. Leaving without drawing it puts it back.
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

function Harness({ onState }: { onState: (s: { edges: Edge[]; modified: boolean }) => void }) {
  const [edges, setEdges] = useState<Edge[]>([ORIGINAL, OTHER]);
  const [modified, setModified] = useState(false);
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
    </div>
  );
}

function mount() {
  const state = { current: { edges: [] as Edge[], modified: false } };
  const view = render(
    <ReactFlowProvider>
      <Harness onState={(s) => (state.current = s)} />
    </ReactFlowProvider>,
  );
  return { state, view };
}

describe("useTourCanvas", () => {
  it("detaches the edge unsaved, and puts it back", () => {
    const { state } = mount();
    act(() => tourCanvas.detachEdge("review", "fix_until_green"));
    expect(state.current.edges).toEqual([OTHER]);
    expect(state.current.modified).toBe(false);
    act(() => tourCanvas.reattachEdge());
    expect(state.current.edges.map((e) => e.id).sort()).toEqual([ORIGINAL.id, OTHER.id].sort());
    expect(state.current.modified).toBe(false);
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
});
