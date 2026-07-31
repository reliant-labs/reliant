/**
 * useExecutionStatus - Hook for building workflow execution status map
 *
 * Maps workflow node IDs to their execution status for visual highlighting.
 *
 * SOURCE-OF-TRUTH SPLIT (Phase 2):
 * - STATUS (running / completed / failed) is authoritative from the
 *   node_execution STREAM (chatStore.nodeExecutions, reduced by
 *   useNodeExecutionStatus). The server mints a stable identity per node
 *   execution and both streams live events and persists them (so historical
 *   chats replay them on snapshot). This replaces the old GUESSWORK that
 *   inferred status by matching step-id prefixes and by position ("the node
 *   after the last completed one is probably running").
 * - STRUCTURE stays tree-derived: node existence, loop iteration steps/counts,
 *   child-workflow drilling, and step-output linkage all come from the fetched
 *   WorkflowExecution tree, which the stream does not carry.
 *
 * FALLBACK: when a node has no stream event yet (initial load before the first
 * node_execution arrives, or a very old chat whose events predate the persisted
 * stream), status falls back to a FACTUAL tree derivation from that node's own
 * executions (its child workflow, its spawned loop steps, or its direct step
 * records). The fallback deliberately does NOT re-introduce position inference —
 * a node with no evidence of execution simply has no status.
 */

import { useMemo } from 'react'
import type { NodeExecutionStatus } from '../../../lib/workflow-flow'
import type { WorkflowExecution, StepExecution } from '../../Chat/ExecutionSidebar/types'
import {
  useNodeExecutionStatus,
  nodeExecutionKey,
  type StreamNodeStatus,
} from './useNodeExecutionStatus'

/**
 * Loop execution info for a specific loop node
 */
export interface LoopExecutionInfo {
  nodeId: string
  currentIteration?: number       // Current iteration (0-indexed) if running
  completedIterations: number     // Number of completed iterations
  maxIterations?: number          // Max from child workflow or config
  iterationStatuses: NodeExecutionStatus[]  // Status of each iteration
}

/**
 * Extended execution status result
 */
export interface ExecutionStatusResult {
  statusMap: Record<string, NodeExecutionStatus>
  loopInfo: Record<string, LoopExecutionInfo>
}

/**
 * Derive status from a list of step executions
 */
function deriveStatusFromSteps(steps: StepExecution[]): NodeExecutionStatus | undefined {
  if (steps.length === 0) return undefined
  if (steps.some(s => s.status === 'failed')) return 'failed'
  if (steps.some(s => s.status === 'running')) return 'running'
  if (steps.every(s => s.status === 'completed')) return 'completed'
  return 'running' // Some steps exist but status unclear
}

/**
 * Build the execution status result.
 *
 * STATUS comes from `streamStatusByKey` (the reduced node_execution stream,
 * keyed by `${execution.id}:${nodeId}`) — this is authoritative. STRUCTURE
 * (loop iteration grouping, child-workflow linkage) still comes from the tree.
 * When a node has no stream status yet, we fall back to a FACTUAL tree
 * derivation from that node's own executions. There is NO position inference:
 * a node with neither a stream event nor its own execution record has no status.
 */
function buildExecutionStatusResult(
  execution: WorkflowExecution | undefined,
  workflowNodeIds: string[] | undefined,
  streamStatusByKey: Record<string, StreamNodeStatus>,
): ExecutionStatusResult {
  const emptyResult: ExecutionStatusResult = { statusMap: {}, loopInfo: {} }
  if (!execution) {
    return emptyResult
  }

  const statusMap: Record<string, NodeExecutionStatus> = {}
  const loopInfo: Record<string, LoopExecutionInfo> = {}
  const nodeIdSet = new Set(workflowNodeIds || [])
  const nodeOrder = workflowNodeIds || []
  const workflowId = execution.id

  // Workflow start node is always completed once we have an execution.
  statusMap['workflow'] = 'completed'

  // --- STRUCTURE maps (tree-derived; the stream does not carry these) ---

  // 1. Child workflow map (non-inline child workflows), linked by spawnedByNodeId.
  const childWorkflowByNode = new Map<string, WorkflowExecution>()
  for (const child of execution.children) {
    if (child.spawnedByNodeId) {
      const existing = childWorkflowByNode.get(child.spawnedByNodeId)
      if (!existing || child.createdAt > existing.createdAt) {
        childWorkflowByNode.set(child.spawnedByNodeId, child)
      }
    }
  }

  // 2. Spawned steps map (inline execution — loops AND workflow nodes), linked
  //    by loopNodeId which tracks the parent node for all inline execution.
  const spawnedStepsByNode = new Map<string, StepExecution[]>()
  for (const step of execution.steps) {
    if (step.loopNodeId && nodeIdSet.has(step.loopNodeId)) {
      const steps = spawnedStepsByNode.get(step.loopNodeId) || []
      steps.push(step)
      spawnedStepsByNode.set(step.loopNodeId, steps)
    }
  }

  // 3. Direct step map (action nodes), linked by step→node id (see
  //    resolveStepToNode — deterministic LINKAGE, not a status guess). Used only
  //    for the tree fallback when the stream has no status for the node yet.
  const directStepByNode = new Map<string, { failed: boolean; running: boolean }>()
  for (const step of execution.steps) {
    const resolvedNodeId = resolveStepToNode(step.stepId, nodeIdSet)
    if (resolvedNodeId) {
      const existing = directStepByNode.get(resolvedNodeId) || { failed: false, running: false }
      directStepByNode.set(resolvedNodeId, {
        failed: existing.failed || step.status === 'failed',
        running: existing.running || step.status === 'running',
      })
    }
  }

  // --- Per-node status + loop info ---
  for (const nodeId of nodeOrder) {
    const spawnedSteps = spawnedStepsByNode.get(nodeId)

    // Build loop info from spawned steps (groups by iteration). STRUCTURE — the
    // node_execution stream does not carry per-iteration step grouping, so this
    // stays tree-derived.
    if (spawnedSteps && spawnedSteps.length > 0) {
      const byIteration = new Map<number, StepExecution[]>()
      for (const step of spawnedSteps) {
        const iter = step.loopIteration ?? 0
        const iterSteps = byIteration.get(iter) || []
        iterSteps.push(step)
        byIteration.set(iter, iterSteps)
      }

      // Only populate loopInfo if there are multiple iterations (actual loop)
      if (byIteration.size > 1 || (byIteration.size === 1 && byIteration.has(0) === false)) {
        const iterations = Array.from(byIteration.keys()).sort((a, b) => a - b)
        const iterationStatuses = iterations.map(iter =>
          deriveStatusFromSteps(byIteration.get(iter) || []) || 'pending'
        )
        const runningIdx = iterationStatuses.findIndex(s => s === 'running')
        const completedCount = iterationStatuses.filter(s => s === 'completed').length

        loopInfo[nodeId] = {
          nodeId,
          currentIteration: runningIdx >= 0 ? runningIdx : undefined,
          completedIterations: completedCount,
          maxIterations: undefined,
          iterationStatuses,
        }
      }
    }

    // STATUS: stream is authoritative. Fall back to factual tree derivation only
    // when the stream has no event for this node yet.
    const streamStatus = streamStatusByKey[nodeExecutionKey(workflowId, nodeId)]
    if (streamStatus) {
      statusMap[nodeId] = streamStatus
      continue
    }

    // --- Factual tree fallback (no position inference) ---
    const childWorkflow = childWorkflowByNode.get(nodeId)
    const directStep = directStepByNode.get(nodeId)
    if (childWorkflow) {
      statusMap[nodeId] = childWorkflow.status === 'cancelled' ? 'failed' :
                          childWorkflow.status as NodeExecutionStatus
    } else if (spawnedSteps && spawnedSteps.length > 0) {
      statusMap[nodeId] = deriveStatusFromSteps(spawnedSteps) || 'running'
    } else if (directStep) {
      if (directStep.failed) statusMap[nodeId] = 'failed'
      else if (directStep.running) statusMap[nodeId] = 'running'
      else statusMap[nodeId] = 'completed'
    }
    // else: no stream event and no execution record → no status (was position
    // inference before Phase 2; deliberately removed).
  }

  return { statusMap, loopInfo }
}

/**
 * Resolve a step ID to a workflow node ID.
 *
 * This is deterministic step→node LINKAGE (structure), NOT a status guess: it
 * connects a StepExecution record (whose id may be suffixed, e.g.
 * "call_llm-save") back to the diagram node ("call_llm") so the details panel
 * can list a node's steps and the tree fallback can read a node's own step
 * status. Node STATUS itself comes from the node_execution stream, not from here.
 */
function resolveStepToNode(stepId: string, nodeIds: Set<string>): string | null {
  if (nodeIds.has(stepId)) return stepId
  
  // Prefix matching (e.g., "call_llm-save" -> "call_llm")
  for (const nodeId of nodeIds) {
    if (stepId.startsWith(nodeId + '-') || stepId.startsWith(nodeId + '_')) {
      return nodeId
    }
  }
  
  // Base name extraction
  const baseName = stepId.split('-')[0]
  if (nodeIds.has(baseName)) return baseName
  
  return null
}

/**
 * Hook to compute execution status map for workflow nodes.
 *
 * `chatId` connects the diagram to the authoritative node_execution stream
 * (chatStore.nodeExecutions). When omitted (or null), status derives purely
 * from the tree fallback — used by call sites without a chat context.
 */
export function useExecutionStatus(
  execution?: WorkflowExecution,
  nodeIds?: string[],
  chatId?: string | null,
): Record<string, NodeExecutionStatus> {
  const { statusByKey } = useNodeExecutionStatus(chatId ?? null)
  return useMemo(
    () => buildExecutionStatusResult(execution, nodeIds, statusByKey).statusMap,
    [execution, nodeIds, statusByKey]
  )
}

/**
 * Hook to compute extended execution status including loop iteration info.
 *
 * See useExecutionStatus for the `chatId` / stream-source contract.
 */
export function useExtendedExecutionStatus(
  execution?: WorkflowExecution,
  nodeIds?: string[],
  chatId?: string | null,
): ExecutionStatusResult {
  const { statusByKey } = useNodeExecutionStatus(chatId ?? null)
  return useMemo(
    () => buildExecutionStatusResult(execution, nodeIds, statusByKey),
    [execution, nodeIds, statusByKey]
  )
}

/**
 * Find all step executions for a node ID
 */
export function findStepExecutionsForNode(
  execution: WorkflowExecution | undefined, 
  nodeId: string,
  nodeIds: string[]
): StepExecution[] {
  if (!execution) return []
  
  const nodeIdSet = new Set(nodeIds)
  return execution.steps.filter(s => {
    const resolved = resolveStepToNode(s.stepId, nodeIdSet)
    return resolved === nodeId || s.loopNodeId === nodeId
  }).sort((a, b) => b.createdAt - a.createdAt)
}

/**
 * Find child workflow for a workflow node
 */
export function findChildWorkflow(
  execution: WorkflowExecution | undefined,
  nodeId: string
): WorkflowExecution | undefined {
  if (!execution) return undefined
  return execution.children.find(c => c.spawnedByNodeId === nodeId)
}

/**
 * Find all child workflows for a loop node (all iterations)
 */
export function findLoopIterations(
  execution: WorkflowExecution | undefined,
  nodeId: string
): WorkflowExecution[] {
  if (!execution) return []
  return execution.children
    .filter(c => c.spawnedByNodeId === nodeId)
    .sort((a, b) => (a.iteration ?? 0) - (b.iteration ?? 0))
}

/**
 * Loop iteration info from step executions
 */
export interface LoopIterationInfo {
  iteration: number
  steps: StepExecution[]
  status: 'running' | 'completed' | 'failed'
  earliestCreatedAt: number
  latestCreatedAt: number
}

/**
 * Find all loop iterations for a loop node from step executions
 */
export function findLoopIterationSteps(
  execution: WorkflowExecution | undefined,
  loopNodeId: string
): LoopIterationInfo[] {
  if (!execution) return []
  
  const iterationMap = new Map<number, StepExecution[]>()
  
  for (const step of execution.steps) {
    if (step.loopNodeId === loopNodeId && step.loopIteration !== undefined) {
      const iter = step.loopIteration
      const iterSteps = iterationMap.get(iter) || []
      iterSteps.push(step)
      iterationMap.set(iter, iterSteps)
    }
  }
  
  const iterations: LoopIterationInfo[] = []
  for (const [iteration, steps] of iterationMap.entries()) {
    const sortedSteps = [...steps].sort((a, b) => a.createdAt - b.createdAt)
    
    let status: 'running' | 'completed' | 'failed' = 'completed'
    if (sortedSteps.some(s => s.status === 'failed')) status = 'failed'
    else if (sortedSteps.some(s => s.status === 'running')) status = 'running'
    
    iterations.push({
      iteration,
      steps: sortedSteps,
      status,
      earliestCreatedAt: sortedSteps[0]?.createdAt ?? 0,
      latestCreatedAt: sortedSteps[sortedSteps.length - 1]?.createdAt ?? 0,
    })
  }
  
  return iterations.sort((a, b) => a.iteration - b.iteration)
}