// Copyright (c) 2025 Reliant Labs

/**
 * The canvas's insert-and-connect commands, shared by the "+" on node outputs,
 * the "+" on edges and the selection bar.
 *
 * `useCanvasInsertion` owns one piece of state: the anchor a "+" asked for,
 * held while the step palette is open. Whatever the palette (or the sidebar)
 * then adds goes through `insertNode`, which lands it at that anchor — or
 * after the selection, or after the end of the main path — positioned and
 * wired (see insertPlacement.ts).
 *
 * The context value is stable for the builder's lifetime (callbacks read the
 * latest state through a ref), so every edge that reads it does not re-render
 * on each builder render. The viewer provides no context, which is how its
 * read-only canvas shows no "+".
 */

import { useReactFlow, useStoreApi, type Edge, type Node, type XYPosition } from "@xyflow/react";
import { createContext, useContext, useEffect, useMemo, useRef } from "react";

import { toolCallsDefaultForEdge } from "../executeToolsDefaults";
import {
  connectionProblem,
  defaultSourceHandle,
  planInsert,
  resolveAnchor,
  type InsertAnchor,
} from "./insertPlacement";

export interface CanvasInsertApi {
  /** Open the step palette; the step chosen there lands at `anchor`, connected. */
  openPaletteAt: (anchor: InsertAnchor) => void;
  /** Draw `sourceId` → `targetId`. False (with nothing drawn) when it can't be. */
  connect: (sourceId: string, targetId: string) => boolean;
}

const CanvasInsertContext = createContext<CanvasInsertApi | null>(null);

export const CanvasInsertProvider = CanvasInsertContext.Provider;

/** The insert commands, or null on a read-only canvas. */
export function useCanvasInsertApi(): CanvasInsertApi | null {
  return useContext(CanvasInsertContext);
}

export interface UseCanvasInsertionOptions {
  /** False on a read-only canvas: no "+", no anchored inserts. */
  enabled: boolean;
  nodes: Node[];
  edges: Edge[];
  setNodes: (nodes: Node[]) => void;
  setEdges: (edges: Edge[]) => void;
  takeSnapshot: (nodes: Node[], edges: Edge[]) => void;
  markDirty: () => void;
  selectedNodeId: string | null;
  /** Whether the step palette is open; closing it drops a pending anchor. */
  paletteOpen: boolean;
  /** Open the step palette on its Steps list. */
  openPalette: () => void;
  /** The builder's own edge-drawing (undo snapshot, tool_calls prefill). */
  createEdge: (sourceId: string, targetId: string, sourceHandle?: string) => void;
  /** Where a node goes when there is nothing to attach it to. */
  fallbackPosition: () => XYPosition;
}

export interface CanvasInsertion {
  /** Context value for <CanvasInsertProvider>; null when disabled. */
  api: CanvasInsertApi | null;
  /**
   * Add `node` (its position is replaced) at the pending anchor, else after
   * the selection, else after the end of the main path. Selects it and
   * brings it into view. Returns the node as added.
   */
  insertNode: (node: Node) => Node;
}

/** The config panel covers this much of the canvas's right side once a step is selected. */
const CONFIG_PANEL_WIDTH = 460;

export function useCanvasInsertion(options: UseCanvasInsertionOptions): CanvasInsertion {
  const optionsRef = useRef(options);
  optionsRef.current = options;
  const pendingRef = useRef<InsertAnchor | null>(null);
  const reactFlow = useReactFlow();
  const store = useStoreApi();

  // A "+" anchors only the step chosen from the palette it opened.
  useEffect(() => {
    if (!options.paletteOpen) pendingRef.current = null;
  }, [options.paletteOpen]);

  const core = useMemo(() => {
    /** Pan just enough that `node` is on screen beside the config panel. */
    const reveal = (node: Node) => {
      const { width, height } = store.getState();
      if (!width || !height) return;
      const { x, y, zoom } = reactFlow.getViewport();
      const visibleWidth = width > CONFIG_PANEL_WIDTH * 2 ? width - CONFIG_PANEL_WIDTH : width;
      const size = { width: node.measured?.width ?? 220, height: node.measured?.height ?? 100 };
      const left = node.position.x * zoom + x;
      const top = node.position.y * zoom + y;
      const onScreen = left >= 0 && top >= 0 && left + size.width * zoom <= visibleWidth && top + size.height * zoom <= height;
      if (onScreen) return;
      void reactFlow.setCenter(node.position.x + size.width / 2, node.position.y + size.height / 2, { zoom, duration: 250 });
    };

    const insertNode = (node: Node): Node => {
      const { nodes, edges, selectedNodeId, setNodes, setEdges, takeSnapshot, markDirty, fallbackPosition, enabled } = optionsRef.current;
      const pending = enabled ? pendingRef.current : null;
      pendingRef.current = null;
      takeSnapshot(nodes, edges);
      markDirty();

      const anchor = resolveAnchor({ pending, nodes, edges, selectedNodeId });
      const plan = planInsert({ nodes, edges, node, anchor, fallbackPosition: fallbackPosition() });

      // Run LLM Tool Calls wired after a Call LLM runs that step's calls.
      const incoming = plan.edges.find((edge) => edge.target === node.id);
      const prefilled = incoming && toolCallsDefaultForEdge(incoming.source, node.id, plan.nodes, plan.edges);
      const inserted: Node = {
        ...plan.inserted,
        selected: true,
        data: prefilled ? { ...plan.inserted.data, step: prefilled } : plan.inserted.data,
      };
      // The new step becomes the selection, so the next "Add step" chains
      // after it.
      setNodes(plan.nodes.map((n) => (n.id === inserted.id ? inserted : n.selected ? { ...n, selected: false } : n)));
      setEdges(plan.edges);
      reveal(inserted);
      return inserted;
    };

    const api: CanvasInsertApi = {
      openPaletteAt(anchor) {
        if (!optionsRef.current.enabled) return;
        pendingRef.current = anchor;
        optionsRef.current.openPalette();
      },
      connect(sourceId, targetId) {
        const { nodes, edges, createEdge, enabled } = optionsRef.current;
        if (!enabled) return false;
        const source = nodes.find((n) => n.id === sourceId);
        const target = nodes.find((n) => n.id === targetId);
        if (!source || !target || connectionProblem(source, target, edges)) return false;
        createEdge(sourceId, targetId, defaultSourceHandle(source, edges) ?? undefined);
        return true;
      },
    };

    return { api, insertNode };
  }, [reactFlow, store]);

  return { api: options.enabled ? core.api : null, insertNode: core.insertNode };
}
