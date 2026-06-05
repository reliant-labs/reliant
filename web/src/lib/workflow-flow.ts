/**
 * Utility functions for converting Workflow to ReactFlow elements
 * 
 * These utilities are shared between:
 * - WorkflowBuilder (editing mode)
 * - WorkflowViewer (read-only execution visualization)
 */

import type { Node, Edge } from '@xyflow/react'
import type { Workflow, Edge as WorkflowEdge, Step, SwitchMetadata } from '../types/workflow'
import {
  getStepType,
  getStepInputs,
  parseEdgeSource,
  getSwitchNodeId,
} from '../types/workflow'
import { autoLayoutWorkflow } from './workflow-layout'
import {
  NODE_DIMENSIONS as SHARED_NODE_DIMENSIONS,
  DEFAULT_DIMENSIONS as SHARED_DEFAULT_DIMENSIONS,
  estimateSwitchNodeHeight as sharedEstimateSwitchNodeHeight,
} from './workflow-node-dimensions'

// Structural node types that have their own React Flow components
const STRUCTURAL_FLOW_TYPES = new Set(['run', 'workflow', 'join', 'loop', 'router'])

type ConditionValue = string | { expr?: string } | undefined

type EdgeTarget = string[] | undefined

type LegacyEdgeTarget = string | string[] | undefined

function getConditionExpression(condition: ConditionValue): string {
  if (typeof condition === 'string') {
    return condition
  }
  return condition?.expr ?? ''
}

function toTargetArray(target: LegacyEdgeTarget): string[] {
  if (!target) {
    return []
  }
  return Array.isArray(target) ? target : [target]
}

function getFirstTarget(target: EdgeTarget): string | undefined {
  const targets = toTargetArray(target)
  return targets.length > 0 ? targets[0] : undefined
}

/**
 * Map a step type to a React Flow node type.
 * Structural types (run, workflow, agent, join, loop) map to their own node types.
 * Activity types (call_llm, save_message, etc.) map to actionNode.
 */
export function getFlowNodeType(stepType: string): string {
  if (STRUCTURAL_FLOW_TYPES.has(stepType)) {
    return `${stepType}Node`
  }
  // All activity types use actionNode
  return 'actionNode'
}

/**
 * Execution status for a workflow node
 */
export type NodeExecutionStatus = 'pending' | 'running' | 'completed' | 'failed'

/**
 * Extended node data that includes execution status
 */
export interface FlowNodeData {
  [key: string]: unknown  // Index signature for ReactFlow compatibility
  step?: Step
  label: string
  eventType?: string // For event nodes (workflow start)
  cases?: Array<{ id: string; condition: string; label?: string }> // For switch nodes
  executionStatus?: NodeExecutionStatus
  // Loop-specific execution state
  currentIteration?: number       // Current iteration (0-indexed) if loop is running
  completedIterations?: number    // Number of completed iterations
  maxIterations?: number          // Max iterations from loop config
  iterationStatuses?: NodeExecutionStatus[]  // Status of each iteration
}

/**
 * Result of converting a workflow to ReactFlow elements
 */
export interface WorkflowFlowElements {
  nodes: Node<FlowNodeData>[]
  edges: Edge[]
}

/**
 * Loop execution info for a node
 */
export interface LoopNodeExecutionInfo {
  nodeId: string
  currentIteration?: number       // Current iteration (0-indexed) if loop is running
  completedIterations: number     // Number of completed iterations
  maxIterations?: number          // Max from loop config
  iterationStatuses: NodeExecutionStatus[]  // Status of each iteration
}

/**
 * Options for workflow conversion
 */
export interface ConvertOptions {
  /** Execution status by node ID */
  executionStatus?: Record<string, NodeExecutionStatus>
  /** Loop execution info by node ID */
  loopInfo?: Record<string, LoopNodeExecutionInfo>
  /** Whether nodes should be draggable (default: true for builder, false for viewer) */
  draggable?: boolean
  /** Label for the workflow start node (default: "Workflow Start"). Used for "Loop Start" when entering a loop body. */
  workflowStartLabel?: string
}

// Constants for switch node positioning
const SWITCH_HORIZONTAL_OFFSET = 300  // Increased to avoid overlap with source
const MIN_NODE_GAP = 50  // Increased minimum gap

/**
 * Estimate the height of a switch node based on number of cases
 */
function estimateSwitchNodeHeight(numCases: number): number {
  return sharedEstimateSwitchNodeHeight(numCases)
}

/**
 * Convert workflow edges (with cases) to React Flow edges and switch nodes
 * 
 * - Single case edge: direct edge from source to target
 * - Multi-case edge: creates a switch node with edges to each target
 * 
 * @param executionStatus - Optional map of node IDs to execution status.
 *   Edges inherit status from their source node (taken = source completed/running)
 * @param draggable - Whether generated switch nodes are draggable
 */
export function convertEdgesToFlowElements(
  workflowEdges: WorkflowEdge[],
  existingNodes: Node[],
  savedSwitches?: Record<string, SwitchMetadata>,
  executionStatus?: Record<string, NodeExecutionStatus>,
  draggable: boolean = true
): { edges: Edge[]; switchNodes: Node<FlowNodeData>[] } {
  const flowEdges: Edge[] = []
  const switchNodes: Node<FlowNodeData>[] = []

  for (const edge of workflowEdges) {
    let { nodeId, event } = parseEdgeSource(edge.from || '')

    // Special case: "started" event or "workflow" node maps to the "workflow" node
    // The workflow start can be represented as "started" or "workflow" in the definition
    if (edge.from === 'started' || nodeId === 'started' || edge.from === 'workflow' || nodeId === 'workflow') {
      nodeId = 'workflow'
      event = 'started'
    }

    // Get source node's execution status for edge styling
    const sourceStatus = executionStatus?.[nodeId]

    const cases = edge.cases || []
    const defaultTargets = toTargetArray(edge.default)

    // Simple edge: single case with no default, or no cases with single default
    if (cases.length === 0 && defaultTargets.length === 1) {
      // Just a default target - direct edge
      flowEdges.push({
        id: `${nodeId}-${defaultTargets[0]}`,
        source: nodeId,
        target: defaultTargets[0],
        type: 'custom',
        data: {
          label: event || '',
          sourceEvent: event,
          executionStatus: sourceStatus,
        },
      })
      continue
    }

    if (cases.length === 1 && defaultTargets.length === 0) {
      const edgeCase = cases[0]
      const targetId = getFirstTarget(edgeCase.to)
      if (!targetId) {
        continue
      }
      flowEdges.push({
        id: `${nodeId}-${targetId}`,
        source: nodeId,
        target: targetId,
        type: 'custom',
        data: {
          label: edgeCase.label || event || '',
          sourceEvent: event,
          executionStatus: sourceStatus,
        },
      })
      continue
    }

    // Multiple cases and/or default: create switch node
    const sourceKey = edge.from || nodeId
    const switchId = getSwitchNodeId(sourceKey)

    // Check if we have saved switch metadata
    const savedSwitch = savedSwitches?.[switchId]

    // Find source node position for switch placement
    const sourceNode = existingNodes.find((n) => n.id === nodeId)
    const sourcePos = sourceNode?.position || { x: 200, y: 200 }

    // Build switch cases (including default as a special case for display)
    const switchCases = cases.map((c, idx) => ({
      id: `case-${idx}`,
      condition: getConditionExpression(c.condition),
      label: c.label || '',
    }))

    // Add default case if there are default targets
    // Default case has empty condition and id='default' to match the sourceHandle
    if (defaultTargets.length > 0) {
      switchCases.push({
        id: 'default',
        condition: '',  // Empty condition indicates default case
        label: '',
      })
    }

    // Calculate switch position
    // Position it to the right of the source, and vertically centered
    // relative to where it can connect to its targets
    let switchPosition: { x: number; y: number }
    
    if (savedSwitch?.position && savedSwitch.position.x !== undefined && savedSwitch.position.y !== undefined) {
      switchPosition = { x: savedSwitch.position.x, y: savedSwitch.position.y }
    } else {
      // Find target node positions to center the switch
      const allTargetIds = [...cases.flatMap(c => toTargetArray(c.to)), ...defaultTargets]
      const targetNodes = allTargetIds
        .map(id => existingNodes.find(n => n.id === id))
        .filter((n): n is Node => n !== undefined)
      
      const switchHeight = estimateSwitchNodeHeight(switchCases.length)
      
      if (targetNodes.length > 0) {
        // Calculate vertical center of targets
        const targetYs = targetNodes.map(n => n.position.y)
        const minTargetY = Math.min(...targetYs)
        const maxTargetY = Math.max(...targetYs)
        // Estimate target heights (use average node height)
        const avgTargetHeight = 80
        const targetCenterY = (minTargetY + maxTargetY + avgTargetHeight) / 2
        
        // Position switch centered on targets, but not overlapping source
        const sourceNodeHeight = 100 // Estimate source node height
        const minY = sourcePos.y - switchHeight / 2 + sourceNodeHeight / 2
        
        switchPosition = {
          x: sourcePos.x + SWITCH_HORIZONTAL_OFFSET,
          y: Math.max(minY, targetCenterY - switchHeight / 2),
        }
      } else {
        // No targets found - position relative to source with offset to avoid overlap
        // Offset downward to avoid overlapping the source node
        switchPosition = {
          x: sourcePos.x + SWITCH_HORIZONTAL_OFFSET,
          y: sourcePos.y + MIN_NODE_GAP,
        }
      }
    }

    switchNodes.push({
      id: switchId,
      type: 'switchNode',
      position: switchPosition,
      data: { label: 'Switch', cases: switchCases },
      draggable,
    })

    // Edge from source to switch
    flowEdges.push({
      id: `${nodeId}-${switchId}`,
      source: nodeId,
      target: switchId,
      type: 'custom',
      data: {
        sourceEvent: event,
        executionStatus: sourceStatus,
      },
    })

    // Edges from switch to each conditional case target
    for (let i = 0; i < cases.length; i++) {
      const edgeCase = cases[i]
      const caseTargets = toTargetArray(edgeCase.to)
      for (let targetIndex = 0; targetIndex < caseTargets.length; targetIndex++) {
        const caseTarget = caseTargets[targetIndex]
        flowEdges.push({
          id: `${switchId}-${caseTarget}-case-${i}-${targetIndex}`,
          source: switchId,
          sourceHandle: `case-${i}`,
          target: caseTarget,
          type: 'custom',
          data: {
            label: edgeCase.label || '',
            executionStatus: sourceStatus,
          },
        })
      }
    }

    // Edges from switch to default target(s)
    for (const defaultTarget of defaultTargets) {
      flowEdges.push({
        id: `${switchId}-${defaultTarget}-default`,
        source: switchId,
        sourceHandle: 'default',
        target: defaultTarget,
        type: 'custom',
        data: {
          label: 'default',
          executionStatus: sourceStatus,
        },
      })
    }
  }

  return { edges: flowEdges, switchNodes }
}

/**
 * Convert a Workflow to ReactFlow nodes and edges
 * 
 * This is the main entry point for converting workflow data to visualization format.
 */
export function workflowToFlowElements(
  workflow: Workflow,
  options: ConvertOptions = {}
): WorkflowFlowElements {
  const { executionStatus = {}, loopInfo = {}, draggable = true, workflowStartLabel = 'Workflow Start' } = options

  if (!workflow.nodes || workflow.nodes.length === 0) {
    // Empty workflow - just return the start node
    const workflowNode: Node<FlowNodeData> = {
      id: 'workflow',
      type: 'eventNode',
      position: { x: 50, y: 200 },
      data: {
        eventType: 'started',
        label: workflowStartLabel,
        executionStatus: executionStatus['workflow'],
      },
      draggable,
      deletable: false,
    }
    return { nodes: [workflowNode], edges: [] }
  }

  // Get saved positions from ui.positions or use auto-layout
  const savedPositions = workflow.ui?.positions || {}
  const savedSwitches = workflow.ui?.switches || {}
  const workflowWithLayout = autoLayoutWorkflow(workflow)

  // Filter out any nodes without a type field (defensive against malformed data)
  const validNodes = (workflowWithLayout.nodes || []).filter(n => {
    if (!n.type) {
      console.warn('[workflowToFlowElements] Skipping node without type field:', n.id || 'unknown')
      return false
    }
    return true
  })

  // Layout positions (from autoLayoutWorkflow) - used when workflow has no saved positions (e.g. assistant-created)
  const layoutPositions = workflowWithLayout.ui?.positions || {}

  // Convert workflow nodes to React Flow nodes
  const stepNodes: Node<FlowNodeData>[] = validNodes.map((step) => {
    const stepType = getStepType(step)
    const stepId = step.id || ''
    // Prioritize saved positions, then layout positions (neat hierarchy), then random fallback
    // Note: positions are stored in workflow.ui.positions, not on Step
    const savedPos = savedPositions[stepId]
    const layoutPos = layoutPositions[stepId] as { x: number; y: number } | undefined
    const position = (savedPos?.x !== undefined && savedPos?.y !== undefined)
      ? { x: savedPos.x, y: savedPos.y }
      : (layoutPos?.x !== undefined && layoutPos?.y !== undefined)
        ? layoutPos
        : { x: Math.random() * 400 + 100, y: Math.random() * 400 + 100 }
    
    // Get loop info if this is a loop node
    const nodeLoopInfo = loopInfo[stepId]
    
    return {
      id: stepId,
      type: getFlowNodeType(stepType),
      position,
      data: {
        step,
        label: stepId,
        executionStatus: executionStatus[stepId],
        // Loop-specific execution state
        ...(nodeLoopInfo && {
          currentIteration: nodeLoopInfo.currentIteration,
          completedIterations: nodeLoopInfo.completedIterations,
          maxIterations: nodeLoopInfo.maxIterations,
          iterationStatuses: nodeLoopInfo.iterationStatuses,
        }),
      },
      draggable,
    }
  })

  // Saved switch nodes are not rendered directly.
  // They are only used by convertEdgesToFlowElements to restore switch positions/case UI
  // for switches that are still referenced by workflow edges.

  // Create the workflow entry point node
  // Accept "workflow" or legacy "started" position key (inline-edit bodies may use either)
  const savedWorkflowPos =
    workflowWithLayout.ui?.positions?.['workflow'] ?? workflowWithLayout.ui?.positions?.['started']
  const workflowNodePosition = (savedWorkflowPos?.x !== undefined && savedWorkflowPos?.y !== undefined)
    ? { x: savedWorkflowPos.x, y: savedWorkflowPos.y }
    : { x: 50, y: 200 }
  const workflowNode: Node<FlowNodeData> = {
    id: 'workflow',
    type: 'eventNode',
    position: workflowNodePosition,
    data: {
      eventType: 'started',
      label: workflowStartLabel,
      executionStatus: executionStatus['workflow'],
    },
    draggable,
    deletable: false,
  }

  // Create entry edges from the entry field if no explicit workflow edges exist
  // This handles workflows that use `entry: node_id` instead of explicit edges
  const workflowEdges = workflowWithLayout.edges || []
  const hasWorkflowStartEdge = workflowEdges.some(
    (e) => e.from === 'workflow' || e.from === 'started'
  )
  
  const edgesWithEntry = [...workflowEdges]
  if (!hasWorkflowStartEdge && workflow.entry) {
    // Create synthetic edges from entry field
    const entryNodes = Array.isArray(workflow.entry) ? workflow.entry : [workflow.entry]
    for (const entryNode of entryNodes) {
      edgesWithEntry.push({
        from: 'workflow',
        cases: [{ to: [entryNode] }],
      })
    }
  }

  // Convert workflow edges to flow edges and switch nodes
  const allNodes = [workflowNode, ...stepNodes]
  const { edges: loadedEdges, switchNodes: generatedSwitchNodes } = convertEdgesToFlowElements(
    edgesWithEntry,
    allNodes,
    savedSwitches,
    executionStatus,
    draggable
  )

  // Use only generated switch nodes (derived from actual edges).
  // Saved switch metadata is already applied inside convertEdgesToFlowElements.
  const allSwitchNodes = generatedSwitchNodes

  // Apply execution status to switch nodes if provided
  const switchNodesWithStatus = allSwitchNodes.map((node) => ({
    ...node,
    data: {
      ...node.data,
      executionStatus: executionStatus[node.id],
    },
  }))

  // Combine all nodes and resolve any remaining overlaps
  const allFinalNodes = [workflowNode, ...stepNodes, ...switchNodesWithStatus]
  const resolvedNodes = resolveNodeOverlaps(allFinalNodes)

  return {
    nodes: resolvedNodes,
    edges: loadedEdges,
  }
}

/**
 * Node dimensions for collision detection — derived from the shared module.
 * flowTypeToLayoutKey maps "runNode" → "run", etc.
 * expandedLoopNode is not in the shared module (only used here for collision detection).
 */
const NODE_DIMENSIONS: Record<string, { width: number; height: number }> = {
  eventNode: { width: SHARED_NODE_DIMENSIONS.event.width, height: SHARED_NODE_DIMENSIONS.event.height },
  runNode: { width: SHARED_NODE_DIMENSIONS.run.width, height: SHARED_NODE_DIMENSIONS.run.height },
  actionNode: { width: SHARED_NODE_DIMENSIONS.action.width, height: SHARED_NODE_DIMENSIONS.action.height },
  workflowNode: { width: SHARED_NODE_DIMENSIONS.workflow.width, height: SHARED_NODE_DIMENSIONS.workflow.height },
  joinNode: { width: SHARED_NODE_DIMENSIONS.join.width, height: SHARED_NODE_DIMENSIONS.join.height },
  loopNode: { width: SHARED_NODE_DIMENSIONS.loop.width, height: SHARED_NODE_DIMENSIONS.loop.height },
  expandedLoopNode: { width: 400, height: 300 },
  switchNode: { width: SHARED_NODE_DIMENSIONS.switch.width, height: SHARED_NODE_DIMENSIONS.switch.height },
  routerNode: { width: SHARED_NODE_DIMENSIONS.router.width, height: SHARED_NODE_DIMENSIONS.router.height },
}

const DEFAULT_NODE_DIMS = { width: SHARED_DEFAULT_DIMENSIONS.width, height: SHARED_DEFAULT_DIMENSIONS.height }
const MIN_NODE_SPACING = 30  // Minimum gap between nodes

interface NodeBounds {
  id: string
  x: number
  y: number
  width: number
  height: number
  type: string
}

/**
 * Get the bounds of a node including estimated dimensions
 */
function getNodeBounds(node: Node, position: { x: number; y: number }): NodeBounds {
  const dims = NODE_DIMENSIONS[node.type || ''] || DEFAULT_NODE_DIMS
  
  // Special handling for switch nodes - height depends on number of cases
  let height = dims.height
  const nodeData = node.data as Record<string, unknown>
  if (node.type === 'switchNode' && nodeData.cases) {
    const cases = nodeData.cases as unknown[]
    height = estimateSwitchNodeHeight(cases.length)
  }
  
  // Special handling for workflow nodes with inputs
  if (node.type === 'workflowNode' && nodeData.step) {
    const stepInputs = getStepInputs(nodeData.step as Step)
    if (Object.keys(stepInputs).length > 0) {
      height = 160 + Math.min(Object.keys(stepInputs).length * 20, 100)
    }
  }
  
  return {
    id: node.id,
    x: position.x,
    y: position.y,
    width: dims.width,
    height,
    type: node.type || 'unknown',
  }
}

/**
 * Check if two bounding boxes overlap (with spacing)
 */
function boundsOverlap(a: NodeBounds, b: NodeBounds): boolean {
  return !(
    a.x + a.width + MIN_NODE_SPACING < b.x ||
    b.x + b.width + MIN_NODE_SPACING < a.x ||
    a.y + a.height + MIN_NODE_SPACING < b.y ||
    b.y + b.height + MIN_NODE_SPACING < a.y
  )
}

/**
 * Calculate how much to move node B to resolve overlap with node A
 * Returns the Y adjustment needed (positive = move down)
 */
function resolveOverlap(fixed: NodeBounds, toMove: NodeBounds): number {
  // If they don't overlap horizontally, no adjustment needed
  const horizontalOverlap = !(
    fixed.x + fixed.width + MIN_NODE_SPACING < toMove.x ||
    toMove.x + toMove.width + MIN_NODE_SPACING < fixed.x
  )
  
  if (!horizontalOverlap) return 0
  
  // Calculate vertical overlap
  const fixedBottom = fixed.y + fixed.height + MIN_NODE_SPACING
  if (toMove.y < fixedBottom) {
    return fixedBottom - toMove.y
  }
  
  return 0
}

/**
 * Resolve overlapping nodes by adjusting their positions
 * Uses 2D bounding box collision detection
 * Exported for use in WorkflowBuilder
 */
export function resolveNodeOverlaps<T extends Node = Node>(nodes: T[]): T[] {
  if (nodes.length <= 1) return nodes
  
  // Create mutable position copies
  const nodePositions = new Map<string, { x: number; y: number }>()
  nodes.forEach(n => nodePositions.set(n.id, { ...n.position }))
  
  // Build initial bounds for all nodes
  const bounds: NodeBounds[] = nodes.map(n => 
    getNodeBounds(n, nodePositions.get(n.id)!)
  )
  
  // Sort by X position (left to right), then by Y position (top to bottom)
  // This establishes priority - nodes further right will move if they overlap
  bounds.sort((a, b) => {
    if (Math.abs(a.x - b.x) < 50) {
      // Same approximate X - sort by Y
      return a.y - b.y
    }
    return a.x - b.x
  })
  
  // Iteratively resolve overlaps
  // We do multiple passes because moving one node might create new overlaps
  const maxIterations = 20
  for (let iter = 0; iter < maxIterations; iter++) {
    let hasOverlap = false
    
    // Check all pairs for overlap
    for (let i = 0; i < bounds.length; i++) {
      for (let j = i + 1; j < bounds.length; j++) {
        const a = bounds[i]
        const b = bounds[j]
        
        if (boundsOverlap(a, b)) {
          hasOverlap = true
          
          // Move the node that's further right (or lower if same X) down
          const adjustment = resolveOverlap(a, b)
          if (adjustment > 0) {
            b.y += adjustment
            const pos = nodePositions.get(b.id)!
            pos.y = b.y
          }
        }
      }
    }
    
    if (!hasOverlap) break
  }
  
  // Apply resolved positions to nodes
  return nodes.map(node => {
    const pos = nodePositions.get(node.id)
    if (pos && (pos.x !== node.position.x || pos.y !== node.position.y)) {
      return {
        ...node,
        position: pos,
      }
    }
    return node
  })
}

// ============================================================================
// EXPANDED LOOP SUPPORT
// ============================================================================

/**
 * Configuration for an expanded loop's child nodes
 */
export interface ExpandedLoopConfig {
  /** The loop node ID that is expanded */
  loopNodeId: string
  /** The sub-workflow definition */
  subWorkflow: Workflow
  /** Execution status for child nodes (from selected iteration) */
  childExecutionStatus?: Record<string, NodeExecutionStatus>
  /** Whether the loop itself is running */
  loopIsRunning?: boolean
  /** Currently selected iteration (for display) */
  selectedIteration?: number
  /** Total iterations available */
  totalIterations?: number
  /** Status of each iteration for displaying tabs */
  iterationStatuses?: NodeExecutionStatus[]
}

/**
 * Result of generating expanded loop elements
 */
export interface ExpandedLoopElements {
  /** Child nodes to add (with parentId set) */
  childNodes: Node<FlowNodeData>[]
  /** Internal edges between child nodes */
  childEdges: Edge[]
  /** The modified parent loop node (resized to group) */
  parentNode: Node<FlowNodeData>
}

/** Padding inside the expanded loop group */
const LOOP_GROUP_PADDING = 60
/** Header height for iteration tabs */
const LOOP_GROUP_HEADER = 60
/** Minimum size for the loop group */
const LOOP_GROUP_MIN_WIDTH = 400
const LOOP_GROUP_MIN_HEIGHT = 300

/**
 * Generate child nodes and edges for an expanded loop
 * 
 * Converts a sub-workflow into nodes that are children of the loop group node.
 * Child nodes have parentId set to the loop node, making them render inside.
 */
export function generateExpandedLoopElements(
  loopNode: Node<FlowNodeData>,
  config: ExpandedLoopConfig
): ExpandedLoopElements {
  const { loopNodeId, subWorkflow, childExecutionStatus = {} } = config
  
  // Ensure sub-workflow has proper layout before converting to flow elements
  // If sub-workflow doesn't have positions, apply auto-layout
  // Note: positions are stored in workflow.ui.positions, not on individual nodes
  const hasPositions = subWorkflow.ui?.positions && Object.keys(subWorkflow.ui.positions).length > 0
  const layoutedSubWorkflow = hasPositions ? subWorkflow : autoLayoutWorkflow(subWorkflow)
  
  // Convert sub-workflow to flow elements
  const subElements = workflowToFlowElements(layoutedSubWorkflow, {
    executionStatus: childExecutionStatus,
    draggable: false, // Child nodes not draggable in viewer
  })
  

  // Calculate bounds of child nodes for group sizing
  let minX = Infinity, minY = Infinity, maxX = -Infinity, maxY = -Infinity
  for (const node of subElements.nodes) {
    // Estimate node size (we'll use fixed estimates based on node type)
    const nodeType = node.type || 'actionNode'
    const nodeDims = NODE_DIMENSIONS[nodeType] || DEFAULT_NODE_DIMS
    const nodeWidth = nodeDims.width
    const nodeHeight = nodeDims.height
    minX = Math.min(minX, node.position.x)
    minY = Math.min(minY, node.position.y)
    maxX = Math.max(maxX, node.position.x + nodeWidth)
    maxY = Math.max(maxY, node.position.y + nodeHeight)
  }

  // Handle empty sub-workflow
  if (!isFinite(minX)) {
    minX = 0
    minY = 0
    maxX = LOOP_GROUP_MIN_WIDTH - LOOP_GROUP_PADDING * 2
    maxY = LOOP_GROUP_MIN_HEIGHT - LOOP_GROUP_PADDING * 2 - LOOP_GROUP_HEADER
  }

  // Calculate child content dimensions
  const contentWidth = maxX - minX
  const contentHeight = maxY - minY

  // Group dimensions (content + padding + header)
  // Add extra padding to ensure nodes aren't too close to edges
  const groupWidth = Math.max(LOOP_GROUP_MIN_WIDTH, contentWidth + LOOP_GROUP_PADDING * 2)
  const groupHeight = Math.max(LOOP_GROUP_MIN_HEIGHT, contentHeight + LOOP_GROUP_PADDING * 2 + LOOP_GROUP_HEADER)

  // Offset to apply to child positions (relative to group top-left)
  // Always center the content horizontally for better visual balance
  // Vertically, add padding below header
  const availableWidth = groupWidth - LOOP_GROUP_PADDING * 2
  const offsetX = (availableWidth - contentWidth) / 2 + LOOP_GROUP_PADDING - minX
  const offsetY = LOOP_GROUP_PADDING + LOOP_GROUP_HEADER - minY

  // Create child nodes with parentId and adjusted positions
  // Ensure node type is preserved so they render with proper styling
  const childNodes: Node<FlowNodeData>[] = subElements.nodes.map(node => ({
    ...node,
    id: `${loopNodeId}:${node.id}`, // Prefix to avoid ID collision
    type: node.type || 'actionNode', // Explicitly preserve node type for proper rendering
    parentId: loopNodeId,
    extent: 'parent' as const, // Keep children inside parent bounds
    position: {
      x: node.position.x + offsetX,
      y: node.position.y + offsetY,
    },
    draggable: false,
    selectable: true,
    data: {
      ...node.data,
      // Child execution status comes from the iteration
      executionStatus: childExecutionStatus[node.id] || node.data.executionStatus,
    },
  }))

  // Create internal edges with prefixed IDs
  const childEdges: Edge[] = subElements.edges.map(edge => ({
    ...edge,
    id: `${loopNodeId}:${edge.id}`,
    source: `${loopNodeId}:${edge.source}`,
    target: `${loopNodeId}:${edge.target}`,
  }))

  // Create the expanded parent node (group type)
  const parentNode: Node<FlowNodeData> = {
    ...loopNode,
    type: 'expandedLoopNode', // Special node type for expanded loops
    style: {
      width: groupWidth,
      height: groupHeight,
    },
    data: {
      ...loopNode.data,
      // Add expanded state info
      isExpanded: true,
      selectedIteration: config.selectedIteration ?? 0,
      totalIterations: config.totalIterations ?? 0,
      subWorkflowName: subWorkflow.name,
      // Pass iteration statuses for tab display
      iterationStatuses: config.iterationStatuses ?? [],
    } as FlowNodeData & {
      isExpanded: boolean
      selectedIteration: number
      totalIterations: number
      subWorkflowName: string
      iterationStatuses: NodeExecutionStatus[]
    },
  }

  return {
    childNodes,
    childEdges,
    parentNode,
  }
}

/**
 * Merge expanded loop elements into existing flow elements
 * 
 * Replaces the original loop node with the expanded version,
 * and adds child nodes/edges.
 */
export function mergeExpandedLoops(
  baseElements: WorkflowFlowElements,
  expandedLoops: ExpandedLoopConfig[]
): WorkflowFlowElements {
  // Start fresh from baseElements - this ensures we always have original nodes and ALL edges
  let nodes = [...baseElements.nodes]
  let edges = [...baseElements.edges]

  // Expand parent loops before descendants so nested IDs like "outer:inner"
  // always have their parent container materialized first.
  const sortedExpandedLoops = [...expandedLoops].sort(
    (a, b) => a.loopNodeId.split(':').length - b.loopNodeId.split(':').length
  )

  // Process each expanded loop against the progressively merged node graph
  for (const config of sortedExpandedLoops) {
    // Find the loop node to expand in current merged nodes (not just the root graph)
    const loopNodeIndex = nodes.findIndex(n => n.id === config.loopNodeId)
    if (loopNodeIndex === -1) continue

    const loopNode = nodes[loopNodeIndex]

    // Generate expanded elements
    const expanded = generateExpandedLoopElements(loopNode, config)

    // Replace the loop node with the expanded version
    nodes[loopNodeIndex] = expanded.parentNode

    // Remove any existing child nodes/edges for this loop (in case of re-expansion)
    nodes = nodes.filter(n => {
      if (n.id === config.loopNodeId) return true // Keep parent
      if (n.id.startsWith(`${config.loopNodeId}:`)) return false // Remove children
      return true // Keep others
    })

    edges = edges.filter(e => !e.id.startsWith(`${config.loopNodeId}:`))

    // Add child nodes (after the parent so they render on top)
    nodes = [...nodes, ...expanded.childNodes]

    // Add child edges
    edges = [...edges, ...expanded.childEdges]
  }

  return { nodes, edges }
}