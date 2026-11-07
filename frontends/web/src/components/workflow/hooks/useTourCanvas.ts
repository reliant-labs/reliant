/**
 * The builder's side of `tourCanvas` (canvas/tourCanvas.ts): the onboarding
 * tour pans the canvas to the steps a card explains, and "Try it yourself"
 * takes one edge out for the user to draw again.
 *
 * A detached edge is only HIDDEN (React Flow's `hidden`), never taken out of
 * the builder's edges, so nothing that reads the workflow can lose it: save
 * (`nodesEdgesToWorkflow`), undo snapshots and a builder that unmounts
 * mid-exercise all still hold the original edge. Hiding it doesn't mark the
 * workflow modified, and neither does putting it back, so the modified flag
 * after "Put it back" is exactly the user's own edits. When the user draws it
 * again, their new edge is dropped for the original, and the flag goes back to
 * what it was just before the draw.
 */

import { useEffect, useRef, type Dispatch, type RefObject, type SetStateAction } from "react";
import { useReactFlow, type Edge } from "@xyflow/react";

import { tourEvents } from "../../Onboarding/tourEvents";
import { tourCanvas, unionRect, viewportForFocus, type Rect, type TourCanvasFocus } from "../canvas/tourCanvas";

interface UseTourCanvasArgs {
  wrapperRef: RefObject<HTMLDivElement | null>;
  /** The first fit has run: a focus applied before it would be overwritten. */
  isViewReady: boolean;
  setEdges: Dispatch<SetStateAction<Edge[]>>;
  hasModifications: boolean;
  setHasModifications: (modified: boolean) => void;
}

export function useTourCanvas({ wrapperRef, isViewReady, setEdges, hasModifications, setHasModifications }: UseTourCanvasArgs): void {
  const { getNodes, setViewport } = useReactFlow();
  const modifiedRef = useRef(hasModifications);
  modifiedRef.current = hasModifications;
  /** The id of the edge lent to the user (hidden), if any. */
  const detachedRef = useRef<{ id: string; source: string; target: string } | null>(null);

  const readyRef = useRef(isViewReady);
  readyRef.current = isViewReady;

  useEffect(() => {
    const apply = (focus: TourCanvasFocus): boolean => {
      const wrapper = wrapperRef.current;
      if (!wrapper || !readyRef.current) return false;
      const wanted = new Set(focus.nodeIds);
      const rects: Rect[] = getNodes()
        .filter((node) => wanted.has(node.id) && !node.parentId)
        .map((node) => ({
          x: node.position.x,
          y: node.position.y,
          width: node.measured?.width ?? node.width ?? 200,
          height: node.measured?.height ?? node.height ?? 100,
        }));
      const box = unionRect(rects);
      if (!box || rects.length < wanted.size) return false;
      const viewport = viewportForFocus(
        box,
        { width: wrapper.clientWidth, height: wrapper.clientHeight },
        focus.reserve,
        { padding: focus.padding, maxZoom: focus.maxZoom },
      );
      void setViewport(viewport, focus.animate ? { duration: 300 } : undefined);
      return true;
    };

    const unsubscribe = tourCanvas.subscribe((command) => {
      switch (command.kind) {
        case "focus":
          apply(command.focus);
          break;
        case "detachEdge":
          setEdges((edges) => {
            const edge = edges.find((e) => e.source === command.source && e.target === command.target && !e.hidden);
            if (!edge || detachedRef.current) return edges;
            detachedRef.current = { id: edge.id, source: edge.source, target: edge.target };
            return edges.map((e) => (e === edge ? { ...e, hidden: true } : e));
          });
          break;
        case "reattachEdge": {
          const detached = detachedRef.current;
          if (!detached) break;
          detachedRef.current = null;
          setEdges((edges) => showEdge(edges, detached.id));
          break;
        }
      }
    });

    // Drawn again: drop the user's new edge for the original, and give the
    // modified flag back the value it had just before the draw (createEdge
    // marks it modified in the same tick), so earlier edits stay marked.
    const offConnected = tourEvents.on("workflow:edgeConnected", ({ sourceId, targetId }) => {
      const detached = detachedRef.current;
      if (!detached || detached.source !== sourceId || detached.target !== targetId) return;
      detachedRef.current = null;
      const modifiedBeforeDraw = modifiedRef.current;
      setEdges((edges) =>
        showEdge(
          edges.filter((e) => e.id === detached.id || !(e.source === sourceId && e.target === targetId)),
          detached.id,
        ),
      );
      setTimeout(() => setHasModifications(modifiedBeforeDraw), 0);
    });

    const onResize = () => {
      const pending = tourCanvas.pendingFocus();
      if (pending) apply({ ...pending, animate: false });
    };
    window.addEventListener("resize", onResize);

    return () => {
      unsubscribe();
      offConnected();
      window.removeEventListener("resize", onResize);
    };
  }, [getNodes, setViewport, setEdges, setHasModifications, wrapperRef]);

  // A focus asked for before the canvas was ready (the tour opened the page).
  useEffect(() => {
    if (!isViewReady) return;
    const pending = tourCanvas.pendingFocus();
    if (!pending) return;
    // Give React Flow a frame to measure the nodes it just placed.
    const frame = requestAnimationFrame(() => {
      const wrapper = wrapperRef.current;
      if (!wrapper) return;
      tourCanvas.focus({ ...pending, animate: false });
    });
    return () => cancelAnimationFrame(frame);
  }, [isViewReady, wrapperRef]);
}

/** `edges` with edge `id` visible again (its `hidden` key removed, as it was before the tour hid it). */
function showEdge(edges: Edge[], id: string): Edge[] {
  return edges.map((e) => {
    if (e.id !== id || !e.hidden) return e;
    const { hidden: _hidden, ...shown } = e;
    return shown;
  });
}
