/**
 * The builder's side of `tourCanvas` (canvas/tourCanvas.ts): the onboarding
 * tour pans the canvas to the steps a card explains, and "Try it yourself"
 * takes one edge out for the user to draw again.
 *
 * A detached edge is never saved: taking it out doesn't mark the workflow
 * modified, and when the user draws it again (or the tour puts it back) the
 * original edge returns with the modified flag as it was, so the user's
 * workflow ends exactly as stored.
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
  const detachedRef = useRef<{ edge: Edge; wasModified: boolean } | null>(null);

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
            const edge = edges.find((e) => e.source === command.source && e.target === command.target);
            if (!edge || detachedRef.current) return edges;
            detachedRef.current = { edge, wasModified: modifiedRef.current };
            return edges.filter((e) => e !== edge);
          });
          break;
        case "reattachEdge": {
          const detached = detachedRef.current;
          if (!detached) break;
          detachedRef.current = null;
          setEdges((edges) => (edges.some((e) => e.id === detached.edge.id) ? edges : [...edges, detached.edge]));
          break;
        }
      }
    });

    // Drawn again: swap the new edge for the original, and restore the modified flag.
    const offConnected = tourEvents.on("workflow:edgeConnected", ({ sourceId, targetId }) => {
      const detached = detachedRef.current;
      if (!detached || detached.edge.source !== sourceId || detached.edge.target !== targetId) return;
      detachedRef.current = null;
      setEdges((edges) => [
        ...edges.filter((e) => !(e.source === sourceId && e.target === targetId)),
        detached.edge,
      ]);
      // createEdge marks the workflow modified in the same tick; undo that after it.
      setTimeout(() => setHasModifications(detached.wasModified), 0);
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
