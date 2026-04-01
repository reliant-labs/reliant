/**
 * useExecutionStatus - Hook for building workflow execution status map
 * 
 * Maps workflow node IDs to their execution status for visual highlighting.
 * 
 * STATUS SOURCES (in priority order):
 * 1. Child workflows (via spawnedByNodeId) - for non-inline child workflows
 * 2. Spawned steps (via loopNodeId) - for inline loops AND inline workflow nodes
 * 3. Direct step executions (via stepId matching) - for action nodes
 * 4. Position inference - next node after last completed is likely running
 */

import { useMemo } from 'react'
import type { NodeExecutionStatus } from '../../../lib/workflow-flow'
import type { WorkflowExecution, StepExecution } from '../../Chat/ExecutionSidebar/types'

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
 * Build execution status map from WorkflowExecution
 */
function buildExecutionStatusResult(
  execution?: WorkflowExecution,
  workflowNodeIds?: string[]
): ExecutionStatusResult {
  const emptyResult: ExecutionStatusResult = { statusMap: {}, loopInfo: {} }
  if (!execution) {
    return emptyResult
  }

  const statusMap: Record<string, NodeExecutionStatus> = {}
  const loopInfo: Record<string, LoopExecutionInfo> = {}
  const nodeIdSet = new Set(workflowNodeIds || [])
  const nodeOrder = workflowNodeIds || []

  // Workflow start node is always completed once we have an execution
  statusMap['workflow'] = 'completed'

  // 1. Build child workflow map (for non-inline child workflows)
  const childWorkflowByNode = new Map<string, WorkflowExecution>()
  for (const child of execution.children) {
    if (child.spawnedByNodeId) {
      const existing = childWorkflowByNode.get(child.spawnedByNodeId)
      if (!existing || child.createdAt > existing.createdAt) {
        childWorkflowByNode.set(child.spawnedByNodeId, child)
      }
    }
  }

  // 2. Build spawned steps map (for inline execution - loops AND workflow nodes)
  // Key insight: loopNodeId tracks the parent node for ALL inline execution
  const spawnedStepsByNode = new Map<string, StepExecution[]>()
  for (const step of execution.steps) {
    if (step.loopNodeId && nodeIdSet.has(step.loopNodeId)) {
      const steps = spawnedStepsByNode.get(step.loopNodeId) || []
      steps.push(step)
      spawnedStepsByNode.set(step.loopNodeId, steps)
    }
  }

  // 3. Build direct step completion map (for action nodes)
  const completedSteps = new Map<string, { time: number; failed: boolean }>()
  for (const step of execution.steps) {
    const resolvedNodeId = resolveStepToNode(step.stepId, nodeIdSet)
    if (resolvedNodeId) {
      const existing = completedSteps.get(resolvedNodeId)
      const stepFailed = step.status === 'failed'
      if (!existing || step.createdAt > existing.time) {
        completedSteps.set(resolvedNodeId, { 
          time: step.createdAt, 
          failed: stepFailed || (existing?.failed ?? false)
        })
      }
    }
  }

  // Find last completed index for position-based inference
  let lastCompletedIndex = -1
  let lastCompletedTime = 0
  
  for (const [nodeId, info] of completedSteps) {
    const idx = nodeOrder.indexOf(nodeId)
    if (idx >= 0 && info.time > lastCompletedTime) {
      lastCompletedIndex = idx
      lastCompletedTime = info.time
    }
  }
  
  for (const [nodeId, child] of childWorkflowByNode) {
    const idx = nodeOrder.indexOf(nodeId)
    if (idx >= 0 && child.status === 'running' && idx > lastCompletedIndex) {
      lastCompletedIndex = idx - 1
    } else if (idx >= 0 && child.createdAt > lastCompletedTime) {
      lastCompletedIndex = idx
      lastCompletedTime = child.createdAt
    }
  }
  
  for (const [nodeId, steps] of spawnedStepsByNode) {
    const idx = nodeOrder.indexOf(nodeId)
    const latestStep = steps.reduce((a, b) => a.createdAt > b.createdAt ? a : b)
    if (idx >= 0 && latestStep.createdAt > lastCompletedTime) {
      lastCompletedIndex = idx
      lastCompletedTime = latestStep.createdAt
    }
  }

  const workflowIsRunning = execution.status === 'running'
  const workflowFailed = execution.status === 'failed'

  // Assign status to each node
  for (let i = 0; i < nodeOrder.length; i++) {
    const nodeId = nodeOrder[i]
    const childWorkflow = childWorkflowByNode.get(nodeId)
    const spawnedSteps = spawnedStepsByNode.get(nodeId)
    const directCompletion = completedSteps.get(nodeId)

    // Build loop info from spawned steps (groups by iteration)
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

    // Determine node status (priority order)
    if (childWorkflow) {
      // Non-inline child workflow
      statusMap[nodeId] = childWorkflow.status === 'cancelled' ? 'failed' :
                          childWorkflow.status as NodeExecutionStatus
    } else if (spawnedSteps && spawnedSteps.length > 0) {
      // Inline execution (loop or workflow node)
      const derivedStatus = deriveStatusFromSteps(spawnedSteps) || 'running'
      statusMap[nodeId] = derivedStatus
    } else if (directCompletion) {
      // Action node with direct step execution
      statusMap[nodeId] = directCompletion.failed ? 'failed' : 'completed'
    } else if (workflowIsRunning && (i === lastCompletedIndex + 1 || (i === 0 && lastCompletedIndex === -1))) {
      // Position-based inference: next node after last completed
      statusMap[nodeId] = 'running'
    }
  }

  // If workflow failed but no node marked failed, mark the last active node
  if (workflowFailed && !Object.values(statusMap).includes('failed')) {
    const lastNode = nodeOrder[Math.max(0, lastCompletedIndex)]
    if (lastNode) {
      statusMap[lastNode] = 'failed'
    }
  }

  return { statusMap, loopInfo }
}

/**
 * Resolve a step ID to a workflow node ID
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
 * Hook to compute execution status map for workflow nodes
 */
export function useExecutionStatus(
  execution?: WorkflowExecution,
  nodeIds?: string[]
): Record<string, NodeExecutionStatus> {
  return useMemo(
    () => buildExecutionStatusResult(execution, nodeIds).statusMap,
    [execution, nodeIds]
  )
}

/**
 * Hook to compute extended execution status including loop iteration info
 */
export function useExtendedExecutionStatus(
  execution?: WorkflowExecution,
  nodeIds?: string[]
): ExecutionStatusResult {
  return useMemo(
    () => buildExecutionStatusResult(execution, nodeIds),
    [execution, nodeIds]
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
