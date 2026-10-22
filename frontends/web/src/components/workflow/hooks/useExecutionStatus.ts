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
 * a node with no evidence of execution simply has no status. A node with
 * evidence that runs inline (a loop) is read as running while the run is still
 * going and has done nothing since at another node: its rows record only
 * activities that FINISHED, so they never show the loop itself finishing.
 */

import { useMemo } from 'react'
import type { NodeExecutionStatus } from '../../../lib/workflow-flow'
import type { WorkflowExecution, StepExecution } from '../../Chat/ExecutionSidebar/types'
import {
  useNodeExecutionStatus,
  nodeExecutionKey,
  type LoopScopedNodeExecution,
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
  /**
   * Stream executions of nodes inside loops, for buildLoopChildStatus. Not
   * folded into statusMap: a loop body's nodes are not root nodes, and their
   * status only means something per iteration.
   */
  loopScoped: LoopScopedNodeExecution[]
  /** Newest node event sequence of the run's workflow (see reduceNodeExecutions). */
  latestSequence: number | undefined
}

/**
 * Whether a loop-scoped execution is what the run is doing now: running, or
 * the last thing the run did (the gap between two activities of one node).
 */
function isBusy(entry: LoopScopedNodeExecution, latestSequence: number | undefined): boolean {
  return entry.status === 'running' || entry.sequence === latestSequence
}

/** Whether an execution ran inside `scopeNodeId` — directly, or anywhere below it. */
function ranInside(entry: LoopScopedNodeExecution, scopeNodeId: string): boolean {
  return (
    entry.loopNodeId === scopeNodeId ||
    (entry.nodePath !== undefined && entry.nodePath.startsWith(scopeNodeId + '.'))
  )
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
  loopScoped: LoopScopedNodeExecution[],
  latestSequenceByWorkflow: Record<string, number>,
): ExecutionStatusResult {
  if (!execution) {
    return { statusMap: {}, loopInfo: {}, loopScoped, latestSequence: undefined }
  }
  const latestSequence = latestSequenceByWorkflow[execution.id]
  const scopedHere = loopScoped.filter((entry) => entry.workflowId === execution.id)

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

  // 4. When each node last left a record: a step row is written as an
  //    activity finishes, a child workflow exists from the moment it is
  //    spawned. A row from a scope nested below a node (its loopNodeId names
  //    a node of some inner workflow, not of this one) cannot be placed, so
  //    it says nothing about which of this workflow's nodes ran.
  const lastRecordByNode = new Map<string, number>()
  const noteRecord = (nodeId: string, at: number) => {
    if (at > (lastRecordByNode.get(nodeId) ?? -Infinity)) lastRecordByNode.set(nodeId, at)
  }
  for (const step of execution.steps) {
    const owner = step.loopNodeId
      ? nodeIdSet.has(step.loopNodeId) ? step.loopNodeId : null
      : resolveStepToNode(step.stepId, nodeIdSet)
    if (owner) noteRecord(owner, step.createdAt)
  }
  for (const child of execution.children) {
    if (child.spawnedByNodeId && nodeIdSet.has(child.spawnedByNodeId)) {
      noteRecord(child.spawnedByNodeId, child.createdAt)
    }
  }
  /** Whether the run has done anything, at any other node, since `nodeId`'s newest record. */
  const runMovedPast = (nodeId: string): boolean => {
    const last = lastRecordByNode.get(nodeId)
    if (last === undefined) return false
    for (const [otherId, at] of lastRecordByNode) {
      if (otherId !== nodeId && at > last) return true
    }
    return false
  }
  // The stream has not said a word about this run: it is not connected yet,
  // or the chat predates persisted node events. When it has, it knows which
  // node the run is in, and the history below must not second-guess it.
  const streamSilent = latestSequence === undefined

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

    // A loop or sub-workflow node has no activity of its own, so no event of
    // its own: it is running exactly while something inside it is. Its step
    // rows only ever say completed/failed (they are written as steps finish),
    // which is why a running loop used to be drawn as already done.
    if (
      execution.status === 'running' &&
      scopedHere.some((entry) => ranInside(entry, nodeId) && isBusy(entry, latestSequence))
    ) {
      statusMap[nodeId] = 'running'
      continue
    }

    // A node that runs its body inline — a loop, an inline sub-workflow — is
    // recorded only as its activities FINISH, so its rows say completed (or
    // failed) all the way through: iteration 2 is under way the moment
    // iteration 1's last row is written, and a failed check is the normal
    // shape of a review iteration. Its rows therefore cannot show it
    // finished. What does is the run ending, or moving on to another node.
    // Until then, with no stream to ask, the node whose records are the
    // newest thing a running run did is the node it is still in — the same
    // reading isBusy gives the stream for the gap between two activities.
    if (
      streamSilent &&
      execution.status === 'running' &&
      spawnedSteps &&
      spawnedSteps.length > 0 &&
      !runMovedPast(nodeId)
    ) {
      statusMap[nodeId] = 'running'
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

  return { statusMap, loopInfo, loopScoped: scopedHere, latestSequence }
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
  const { statusByKey, loopScoped, latestSequenceByWorkflow } = useNodeExecutionStatus(chatId ?? null)
  return useMemo(
    () =>
      buildExecutionStatusResult(execution, nodeIds, statusByKey, loopScoped, latestSequenceByWorkflow)
        .statusMap,
    [execution, nodeIds, statusByKey, loopScoped, latestSequenceByWorkflow]
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
  const { statusByKey, loopScoped, latestSequenceByWorkflow } = useNodeExecutionStatus(chatId ?? null)
  return useMemo(
    () => buildExecutionStatusResult(execution, nodeIds, statusByKey, loopScoped, latestSequenceByWorkflow),
    [execution, nodeIds, statusByKey, loopScoped, latestSequenceByWorkflow]
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

// ---------------------------------------------------------------------------
// Loop bodies
//
// An expanded loop draws its sub-workflow's nodes once, and shows one
// iteration at a time. Everything below answers "what is the state of child
// node X in iteration N of loop L" from the two sources that know:
//   - step rows (findLoopIterationSteps), written when a step FINISHES — the
//     record of what happened, including exit codes;
//   - the loop-scoped node_execution stream (LoopScopedNodeExecution), which
//     is the only thing that knows a step is running NOW.
// ---------------------------------------------------------------------------

/**
 * Resolve a step id recorded inside a loop body to the body node it belongs
 * to. Step ids carry suffixes ("lint-save", "review_checkpoint" is its own
 * node), so this tries an exact match first and then progressively looser
 * ones. Exact-first is what keeps "review_checkpoint" from resolving to
 * "review".
 */
export function resolveLoopChildNodeId(stepId: string, childNodeIds: Set<string>): string | null {
  if (childNodeIds.has(stepId)) return stepId

  for (const childId of childNodeIds) {
    if (
      stepId.startsWith(childId + '-') ||
      stepId.startsWith(childId + '_') ||
      childId.startsWith(stepId + '-') ||
      childId.startsWith(stepId + '_')
    ) {
      return childId
    }
  }

  const baseName = stepId.split('-')[0].split('_')[0]
  if (childNodeIds.has(baseName)) return baseName

  const withoutSuffix = stepId.replace(/-(save|result|output|input)$/i, '')
  if (childNodeIds.has(withoutSuffix)) return withoutSuffix

  for (const part of stepId.split(/[-_]/)) {
    if (childNodeIds.has(part)) return part
  }

  for (const childId of childNodeIds) {
    if (stepId.includes(childId) || childId.includes(stepId)) return childId
  }
  return null
}

/**
 * The step executions of one node inside a loop body.
 *
 * A body node is drawn with a loop-scoped id ("attempt:review"), which matches
 * nothing in the root workflow — that is why the details panel said "Not yet
 * executed" for every node inside a loop, including the reviewer whose
 * verdict is the point of the run. Rows are matched on the loop they ran in
 * and, when given, the iteration being viewed.
 */
export function findLoopChildStepExecutions(
  execution: WorkflowExecution | undefined,
  loopNodeId: string,
  childNodeId: string,
  childNodeIds: string[],
  iteration?: number,
): StepExecution[] {
  if (!execution) return []
  const ids = new Set(childNodeIds)
  ids.add(childNodeId)
  return execution.steps
    .filter(
      (step) =>
        step.loopNodeId === loopNodeId &&
        (iteration === undefined || step.loopIteration === iteration) &&
        resolveLoopChildNodeId(step.stepId, ids) === childNodeId,
    )
    .sort((a, b) => b.createdAt - a.createdAt)
}

export interface LoopChildStatusInput {
  /** The run's workflow id — loop-scoped stream entries are matched on it. */
  workflowId: string | undefined
  /**
   * The expanded loop's diagram id. A loop nested in another expanded loop is
   * drawn as "outer:inner"; its own id is the last segment.
   */
  loopNodeId: string
  /** Ids of the loop body's nodes. */
  childNodeIds: string[]
  /** This loop's step rows, grouped by iteration (findLoopIterationSteps). */
  iterations: LoopIterationInfo[]
  /** Loop-scoped stream executions (useExtendedExecutionStatus). */
  loopScoped: LoopScopedNodeExecution[]
  /** A finished loop has no running children, whatever a stale event says. */
  loopIsRunning: boolean
  /** Newest node event sequence of the run (ExecutionStatusResult.latestSequence). */
  latestSequence: number | undefined
  /**
   * A parallel loop runs its iterations at once, so an activity that does
   * not name this loop's iteration cannot be placed in one.
   */
  parallel?: boolean
}

export interface LoopChildStatus {
  /** Status of each body node, per iteration. */
  byIteration: Map<number, Record<string, NodeExecutionStatus>>
  /** Per-iteration rollup, index = iteration number (0..latestIteration). */
  iterationStatuses: NodeExecutionStatus[]
  /** The newest iteration the run has reached — the one to follow live. */
  latestIteration: number | undefined
}

const CHILD_STATUS_RANK: Record<NodeExecutionStatus, number> = {
  pending: 0,
  completed: 1,
  failed: 2,
  running: 3,
}

/** Where a stream entry sits relative to the loop: which body node, and how deep. */
function placeInLoop(
  entry: LoopScopedNodeExecution,
  loopPath: string[],
  ownLoopId: string,
  childIds: Set<string>,
): { childId: string; direct: boolean } | null {
  if (entry.nodePath) {
    const segments = entry.nodePath.split('.')
    for (let start = 0; start + loopPath.length < segments.length; start++) {
      if (loopPath.every((segment, i) => segments[start + i] === segment)) {
        const childIndex = start + loopPath.length
        const childId = segments[childIndex]
        if (!childIds.has(childId)) return null
        return { childId, direct: childIndex === segments.length - 1 }
      }
    }
    return null
  }
  // Events written before the server sent node_path: only this loop's own
  // scope can be placed, by the step id.
  if (entry.loopNodeId !== ownLoopId) return null
  const childId = resolveLoopChildNodeId(entry.nodeId, childIds)
  return childId ? { childId, direct: true } : null
}

/**
 * The state of every node in a loop body, per iteration.
 *
 * Rows are the record of what finished — and the only source of a run step's
 * exit code, so a row's "failed" is never overridden by the stream's
 * lifecycle "completed". The stream adds what rows cannot know: that a step is
 * running right now. Two kinds of stream entry contribute:
 *
 *  - Entries scoped to THIS loop carry their iteration, so they are placed
 *    exactly: lint/test/build running in iteration 2 light up in iteration 2.
 *  - Entries from deeper inside a body node — the reviewer's own agent loop,
 *    say — are scoped to that inner loop, not this one, so they carry no
 *    iteration of ours. Their node_path still says WHICH body node they are
 *    in. A body node runs once per iteration, so such an entry belongs to the
 *    newest iteration unless that node has already finished there, in which
 *    case it is the start of the next one. Not done for parallel loops, whose
 *    iterations run side by side.
 *
 * A body node with a deep entry is running while that entry is, and also
 * between its activities — while the run's newest node event is still its
 * own — so it does not flicker off for the moment the engine takes to
 * schedule its next step.
 */
export function buildLoopChildStatus(input: LoopChildStatusInput): LoopChildStatus {
  const { workflowId, loopNodeId, iterations, loopScoped, loopIsRunning, latestSequence, parallel } = input
  const childIds = new Set(input.childNodeIds)
  const loopPath = loopNodeId.split(':')
  const ownLoopId = loopPath[loopPath.length - 1]
  const byIteration = new Map<number, Record<string, NodeExecutionStatus>>()

  const merge = (iteration: number, childId: string, status: NodeExecutionStatus) => {
    if (iteration < 0) return
    const statuses = byIteration.get(iteration) ?? {}
    const current = statuses[childId]
    if (!current || CHILD_STATUS_RANK[status] > CHILD_STATUS_RANK[current]) {
      statuses[childId] = status
    }
    byIteration.set(iteration, statuses)
  }
  const isFinishedAt = (iteration: number, childId: string) => {
    const status = byIteration.get(iteration)?.[childId]
    return status === 'completed' || status === 'failed'
  }

  let latestIteration: number | undefined
  const reach = (iteration: number) => {
    if (iteration >= 0 && (latestIteration === undefined || iteration > latestIteration)) {
      latestIteration = iteration
    }
  }

  // 1. Rows: what finished, per iteration.
  for (const iteration of iterations) {
    for (const step of iteration.steps) {
      const childId = resolveLoopChildNodeId(step.stepId, childIds)
      if (childId) merge(iteration.iteration, childId, step.status)
    }
    if (iteration.steps.length > 0) reach(iteration.iteration)
  }

  // 2. Stream entries placed in this loop's body.
  const placed: Array<{ entry: LoopScopedNodeExecution; childId: string; direct: boolean; ours: boolean }> = []
  for (const entry of loopScoped) {
    if (workflowId && entry.workflowId !== workflowId) continue
    const place = placeInLoop(entry, loopPath, ownLoopId, childIds)
    if (!place) continue
    const ours = entry.loopNodeId === ownLoopId
    placed.push({ entry, ...place, ours })
    if (ours) reach(entry.iteration)
  }

  // Entries that name our iteration: placed exactly. A deep entry only ever
  // says "this body node is busy", never that it finished.
  for (const { entry, childId, direct, ours } of placed) {
    if (!ours) continue
    if (entry.status === 'running') {
      if (loopIsRunning) merge(entry.iteration, childId, 'running')
    } else if (direct) {
      merge(entry.iteration, childId, entry.status)
    }
  }

  // Entries from deeper loops: placed by body node, in the open iteration.
  if (loopIsRunning && !parallel) {
    const open = latestIteration ?? 0
    for (const { entry, childId, ours } of placed) {
      if (ours) continue
      if (!isBusy(entry, latestSequence)) continue
      const iteration = isFinishedAt(open, childId) ? open + 1 : open
      merge(iteration, childId, 'running')
      reach(iteration)
    }
  }

  const iterationStatuses: NodeExecutionStatus[] = []
  if (latestIteration !== undefined) {
    for (let i = 0; i <= latestIteration; i++) {
      const statuses = Object.values(byIteration.get(i) ?? {})
      // An iteration with a step still running is running, even if a check in
      // it already failed — that is the normal shape of a review iteration.
      if (statuses.length === 0) iterationStatuses.push('pending')
      else if (statuses.includes('running')) iterationStatuses.push('running')
      else if (statuses.includes('failed')) iterationStatuses.push('failed')
      else if (statuses.every((s) => s === 'completed')) iterationStatuses.push('completed')
      else iterationStatuses.push('pending')
    }

    // While the loop runs, its newest iteration is the one under way. When
    // nothing above placed a running step in it — no stream, only rows —
    // the rows of what finished so far would read it as done, or as failed
    // over a check the next attempt exists to fix. Not for a parallel loop,
    // whose iterations are all in flight at once.
    if (loopIsRunning && !parallel && !iterationStatuses.includes('running')) {
      iterationStatuses[latestIteration] = 'running'
    }
  }

  return { byIteration, iterationStatuses, latestIteration }
}