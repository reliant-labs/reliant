/**
 * Workflow edge components and type registry
 * 
 * These are shared between WorkflowBuilder and WorkflowViewer
 */

import type { EdgeTypes } from '@xyflow/react'

export { CustomEdge } from './CustomEdge'

import { CustomEdge } from './CustomEdge'

/**
 * Edge type registry for ReactFlow
 * Maps edge type strings to their React components
 */
export const edgeTypes: EdgeTypes = {
  custom: CustomEdge,
}
