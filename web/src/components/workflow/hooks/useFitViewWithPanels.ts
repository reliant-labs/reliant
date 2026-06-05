/**
 * useFitViewWithPanels
 *
 * Owns the fit-view computation that accounts for the floating left sidebar,
 * the chat panel (collapsed/normal/maximized), the config panel (when a node,
 * edge, or settings editor is open), the top header, and the bottom toolbar.
 *
 * Extracted from `WorkflowBuilder.tsx`. Behavior is intentionally identical
 * to the original `fitViewWithPanels` callback — this is a mechanical move,
 * not a refactor.
 *
 * The hook returns a stable `fitViewWithPanels(animate?)` function whose
 * dependencies match those of the original useCallback.
 */

import { useCallback, type RefObject } from "react";
import { useReactFlow } from "@xyflow/react";
import type { PanelSize } from "../WorkflowBuilderChat";

export interface UseFitViewWithPanelsArgs {
  /** Ref to the ReactFlow canvas wrapper (used to read viewport size). */
  wrapperRef: RefObject<HTMLDivElement | null>;
  /** Whether the chat panel is open (affects right-side overlay width). */
  chatPanelOpen: boolean;
  /** Chat panel size ('normal' | 'maximized') — only consulted when open. */
  chatPanelSize: PanelSize;
  /** Whether a node is selected (config panel widens right overlay). */
  hasSelectedNode: boolean;
  /** Whether an edge is selected (config panel widens right overlay). */
  hasSelectedEdge: boolean;
  /** Whether the workflow settings editor is open (also widens right overlay). */
  showSettingsEditor: boolean;
  /** Whether we're currently inside an inline-edit (loop or workflow body). */
  isEditingLoop: boolean;
}

/**
 * Returns `fitViewWithPanels(animate?: boolean)` — animate=true for smooth
 * transitions (user clicks button), false for instant (initial load).
 */
export function useFitViewWithPanels({
  wrapperRef,
  chatPanelOpen,
  chatPanelSize,
  hasSelectedNode,
  hasSelectedEdge,
  showSettingsEditor,
  isEditingLoop,
}: UseFitViewWithPanelsArgs): (animate?: boolean) => void {
  const { setViewport, getNodes } = useReactFlow();

  return useCallback(
    (animate = true) => {
      // Get the canvas wrapper dimensions
      const wrapper = wrapperRef.current;
      const viewportWidth = wrapper?.clientWidth || 1200;
      const viewportHeight = wrapper?.clientHeight || 800;

      // Overlay dimensions (in pixels from viewport edges)
      const SIDEBAR_WIDTH = 220; // Left sidebar
      const CHAT_PANEL_NORMAL = 430; // Chat panel normal
      const CHAT_PANEL_MAXIMIZED = 630; // Chat panel maximized
      const CONFIG_PANEL_WIDTH = 410; // Config panel
      const HEADER_HEIGHT = 70; // Top header
      const TOOLBAR_HEIGHT = 100; // Bottom toolbar

      // Calculate right-side overlay
      let rightOverlay = 40;
      if (chatPanelOpen) {
        rightOverlay =
          chatPanelSize === "maximized"
            ? CHAT_PANEL_MAXIMIZED
            : CHAT_PANEL_NORMAL;
      }
      if (hasSelectedNode || hasSelectedEdge || showSettingsEditor) {
        rightOverlay = Math.max(rightOverlay, CONFIG_PANEL_WIDTH);
      }

      // Calculate the safe zone (area not covered by overlays)
      const safeLeft = SIDEBAR_WIDTH;
      const safeRight = viewportWidth - rightOverlay;
      const safeTop = isEditingLoop ? HEADER_HEIGHT + 40 : HEADER_HEIGHT;
      const safeBottom = viewportHeight - TOOLBAR_HEIGHT;

      const safeWidth = safeRight - safeLeft;
      const safeHeight = safeBottom - safeTop;

      // Get current nodes to calculate bounds
      const currentNodes = getNodes();
      if (currentNodes.length === 0) {
        // No nodes, just use default view
        setViewport(
          { x: 0, y: 0, zoom: 1 },
          animate ? { duration: 200 } : undefined,
        );
        return;
      }

      // Calculate node bounds
      let minX = Infinity,
        minY = Infinity,
        maxX = -Infinity,
        maxY = -Infinity;
      for (const node of currentNodes) {
        const x = node.position.x;
        const y = node.position.y;
        const width = node.measured?.width ?? node.width ?? 200;
        const height = node.measured?.height ?? node.height ?? 100;
        minX = Math.min(minX, x);
        minY = Math.min(minY, y);
        maxX = Math.max(maxX, x + width);
        maxY = Math.max(maxY, y + height);
      }

      const nodesWidth = maxX - minX;
      const nodesHeight = maxY - minY;

      // Add small padding around nodes (in world coordinates)
      const nodePadding = 40;
      const paddedWidth = nodesWidth + nodePadding * 2;
      const paddedHeight = nodesHeight + nodePadding * 2;

      // Calculate zoom to fit nodes in safe zone
      const zoomX = safeWidth / paddedWidth;
      const zoomY = safeHeight / paddedHeight;
      let zoom = Math.min(zoomX, zoomY);

      // Apply zoom constraints
      zoom = Math.max(0.3, Math.min(1.0, zoom)); // Keep between 30% and 100%

      // Calculate viewport position to center nodes in the safe zone
      // The safe zone center in viewport coords
      const safeCenterX = safeLeft + safeWidth / 2;
      const safeCenterY = safeTop + safeHeight / 2;

      // The nodes center in world coords
      const nodesCenterX = minX + nodesWidth / 2;
      const nodesCenterY = minY + nodesHeight / 2;

      // Viewport x,y represents the world coordinate at viewport (0,0)
      // To center nodes in safe zone: safeCenterX = -x * zoom + nodesCenterX * zoom
      // Solving for x: x = nodesCenterX - safeCenterX / zoom
      const x = -nodesCenterX * zoom + safeCenterX;
      const y = -nodesCenterY * zoom + safeCenterY;

      setViewport({ x, y, zoom }, animate ? { duration: 200 } : undefined);
    },
    [
      getNodes,
      setViewport,
      chatPanelOpen,
      chatPanelSize,
      hasSelectedNode,
      hasSelectedEdge,
      showSettingsEditor,
      isEditingLoop,
      wrapperRef,
    ],
  );
}
