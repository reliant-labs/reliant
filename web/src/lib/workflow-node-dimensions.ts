/**
 * Shared node dimension estimates for workflow layout and overlap resolution.
 *
 * Both the auto-layout engine (workflow-layout.ts) and the ReactFlow overlap
 * resolver (workflow-flow.ts) must agree on node sizes. Keeping them in one
 * place prevents drift.
 *
 * Values are *estimates* based on the CSS / component rendering. They don't
 * need to be pixel-perfect — they just need to be close enough that the
 * layout algorithm produces non-overlapping positions.
 */

export interface NodeDims {
  width: number
  height: number
  minHeight: number
}

/**
 * Estimated dimensions keyed by *layout* node type.
 *
 * "Layout type" is the simplified classification used by the layout engine:
 *   - Structural types: run, agent, workflow, join, loop, approval
 *   - Activity types: everything else → "action"
 *   - Special: event (workflow start), switch
 */
export const NODE_DIMENSIONS: Record<string, NodeDims> = {
  // Standard nodes – based on NodeStatusWrapper minWidth: 200, maxWidth: 300
  run: { width: 220, height: 100, minHeight: 70 },
  agent: { width: 220, height: 80, minHeight: 70 },
  action: { width: 220, height: 95, minHeight: 85 },
  workflow: { width: 250, height: 160, minHeight: 70 },
  approval: { width: 220, height: 100, minHeight: 90 },
  join: { width: 180, height: 60, minHeight: 60 },
  // Loop nodes are taller — they show config, progress, etc.
  loop: { width: 280, height: 160, minHeight: 130 },
  // Switch nodes have a header (44px) + cases (40px each)
  switch: { width: 200, height: 170, minHeight: 100 },
  // Event node (workflow start)
  event: { width: 150, height: 60, minHeight: 60 },
}

export const DEFAULT_DIMENSIONS: NodeDims = { width: 220, height: 100, minHeight: 70 }

/**
 * Map a ReactFlow node type string (e.g. "runNode", "actionNode") to the
 * layout key used in NODE_DIMENSIONS.
 */
export function flowTypeToLayoutKey(flowType: string): string {
  return flowType.replace(/Node$/, '')
}

/**
 * Get dimensions for a ReactFlow node type.
 */
export function getDimensionsForFlowType(flowType: string): NodeDims {
  return NODE_DIMENSIONS[flowTypeToLayoutKey(flowType)] || DEFAULT_DIMENSIONS
}

// Layout constants shared between layout and flow modules
export const LAYOUT_CONSTANTS = {
  LAYER_SPACING: 280,
  MIN_NODE_SPACING: 40,
  START_X: 150,
  START_Y: 100,
} as const

// Switch node geometry constants
export const SWITCH_GEOMETRY = {
  HEADER_HEIGHT: 44,
  CASE_HEIGHT: 40,
  PADDING: 8,
} as const

/**
 * Estimate switch node height based on number of cases.
 */
export function estimateSwitchNodeHeight(numCases: number): number {
  return SWITCH_GEOMETRY.HEADER_HEIGHT + numCases * SWITCH_GEOMETRY.CASE_HEIGHT + SWITCH_GEOMETRY.PADDING
}
