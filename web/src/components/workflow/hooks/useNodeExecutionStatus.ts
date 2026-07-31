/**
 * useNodeExecutionStatus - Authoritative node status from the node_execution stream.
 *
 * The server mints a stable identity for every workflow node execution and
 * streams `node_execution` events (started / progress / completed / failed).
 * These are folded into chatStore.nodeExecutions by the store reducer and, for
 * historical chats, replayed on the chat snapshot (they are persisted to
 * chat_updates with entity_id = `${workflow_id}:${node_id}`). This hook reduces
 * those events into an authoritative status map so the workflow diagram no
 * longer has to GUESS node status by matching step-id prefixes and inferring
 * position.
 *
 * KEY: the map is keyed by `${workflowId}:${nodeId}` — the exact identity the
 * server already uses. `node_id` on the event is the workflow step id, which
 * maps 1:1 to the diagram node id (the engine resolves it via FindNode), so no
 * prefix matching is needed here.
 *
 * TWO RUNTIME SHAPES feed the same NodeExecutionUpdate array and must both be
 * handled:
 *   - Live proto path (streaming-grpc convertNodeExecutionEvent): `event_type`
 *     and `status` are NUMERIC proto enum values.
 *   - Persisted / snapshot path (convertChatUpdateData spreads Go's raw JSON):
 *     `event_type` is a STRING ("started" / "completed" / "failed") and
 *     `status` is the numeric enum value.
 * normalizeEventStatus() below collapses both into 'running' | 'completed' |
 * 'failed'.
 */

import { useMemo } from 'react'
import { useChatStore } from '../../../store/chatStore'
import type { NodeExecutionUpdate } from '../../../types/streaming'
import {
  NodeExecutionEventType,
  NodeExecutionStatus as ProtoNodeExecutionStatus,
} from '../../../gen/reliant/v1/streaming_pb'
import type { NodeExecutionStatus } from '../../../lib/workflow-flow'

/** Terminal status of a node — must not be reverted by a late 'running' event. */
export type StreamNodeStatus = Extract<
  NodeExecutionStatus,
  'running' | 'completed' | 'failed'
>

/** Iteration info a node_execution event may carry (loop nodes). */
export interface StreamNodeIteration {
  iteration?: number
  maxIterations?: number
}

/**
 * Reduced, authoritative view of the node_execution stream for one chat.
 * Both maps are keyed by `${workflowId}:${nodeId}`.
 */
export interface NodeExecutionStatusResult {
  /** Node status keyed by `${workflowId}:${nodeId}`. */
  statusByKey: Record<string, StreamNodeStatus>
  /** Iteration info keyed by `${workflowId}:${nodeId}` (present only when carried). */
  iterationByKey: Record<string, StreamNodeIteration>
}

const EMPTY_UPDATES: NodeExecutionUpdate[] = []

/** Compose the stream identity key the server already uses. */
export function nodeExecutionKey(workflowId: string, nodeId: string): string {
  return `${workflowId}:${nodeId}`
}

/**
 * Collapse a node_execution event into a coarse lifecycle status, normalizing
 * the numeric-enum (live) and string (persisted) representations. Returns
 * undefined for pending/unspecified events, which do not set a status.
 */
function normalizeEventStatus(
  update: NodeExecutionUpdate,
): StreamNodeStatus | undefined {
  // The lifecycle event_type is the authoritative transition signal. It arrives
  // as a numeric enum on the live path and as a string on the persisted path.
  const eventType: unknown = update.event_type
  if (typeof eventType === 'number') {
    switch (eventType) {
      case NodeExecutionEventType.STARTED:
      case NodeExecutionEventType.PROGRESS:
        return 'running'
      case NodeExecutionEventType.COMPLETED:
        return 'completed'
      case NodeExecutionEventType.FAILED:
        return 'failed'
    }
  } else if (typeof eventType === 'string') {
    switch (eventType) {
      case 'started':
      case 'progress':
        return 'running'
      case 'completed':
        return 'completed'
      case 'failed':
      case 'cancelled':
        return 'failed'
    }
  }

  // Fallback to the status field (numeric enum on both paths; string handled
  // defensively for any serializer that emits enum names).
  const status: unknown = update.status
  if (typeof status === 'number') {
    switch (status) {
      case ProtoNodeExecutionStatus.RUNNING:
        return 'running'
      case ProtoNodeExecutionStatus.COMPLETED:
        return 'completed'
      case ProtoNodeExecutionStatus.FAILED:
      case ProtoNodeExecutionStatus.CANCELLED:
        return 'failed'
    }
  } else if (typeof status === 'string') {
    const s = status.toLowerCase()
    if (s.includes('running')) return 'running'
    if (s.includes('completed')) return 'completed'
    if (s.includes('failed') || s.includes('cancelled')) return 'failed'
  }

  return undefined
}

function isTerminal(status: StreamNodeStatus): boolean {
  return status === 'completed' || status === 'failed'
}

interface Candidate {
  status: StreamNodeStatus
  seq: number
  terminal: boolean
}

/**
 * Fold a chat's node_execution events into an authoritative status map.
 *
 * INVARIANTS:
 *  - Terminal-status guard: once a node reaches a terminal state
 *    (completed/failed), a later 'running'/'started' event must NOT un-complete
 *    or un-fail it. Mirrors the tool-call terminal guard in chatStore.
 *  - Order-insensitive: precedence is decided by terminal-ness first, then by
 *    sequence_number — never by array position or wall-clock. A terminal beats
 *    a non-terminal; between two terminals the higher sequence_number wins (so
 *    a legitimate completed→failed correction still lands).
 *
 * Pure: does not touch the store; safe to memoize on the input array.
 */
export function reduceNodeExecutions(
  updates: NodeExecutionUpdate[],
): NodeExecutionStatusResult {
  const best = new Map<string, Candidate>()
  const iterationByKey: Record<string, StreamNodeIteration> = {}
  const iterationSeq = new Map<string, number>()

  for (const update of updates) {
    if (!update.node_id) continue
    const key = nodeExecutionKey(update.workflow_id, update.node_id)
    const seq = update.sequence_number ?? 0

    // Capture iteration info from the latest-by-sequence event that carries it.
    if (update.iteration !== undefined || update.max_iterations !== undefined) {
      const prevSeq = iterationSeq.get(key)
      if (prevSeq === undefined || seq >= prevSeq) {
        iterationSeq.set(key, seq)
        iterationByKey[key] = {
          iteration: update.iteration,
          maxIterations: update.max_iterations,
        }
      }
    }

    const status = normalizeEventStatus(update)
    if (!status) continue
    const terminal = isTerminal(status)
    const prev = best.get(key)

    if (!prev) {
      best.set(key, { status, seq, terminal })
      continue
    }
    // Guard: a non-terminal event must never overwrite a terminal one.
    if (prev.terminal && !terminal) continue
    // Upgrade: a terminal event always wins over a non-terminal one.
    if (!prev.terminal && terminal) {
      best.set(key, { status, seq, terminal })
      continue
    }
    // Same terminal-ness: higher sequence_number wins (order-insensitive).
    if (seq >= prev.seq) {
      best.set(key, { status, seq, terminal })
    }
  }

  const statusByKey: Record<string, StreamNodeStatus> = {}
  for (const [key, candidate] of best) {
    statusByKey[key] = candidate.status
  }

  return { statusByKey, iterationByKey }
}

/**
 * Read the node_execution stream for a chat and reduce it to an authoritative
 * status map. Returns stable empty maps when chatId is null or no events exist.
 */
export function useNodeExecutionStatus(
  chatId: string | null,
): NodeExecutionStatusResult {
  const updates = useChatStore((state) =>
    chatId ? state.nodeExecutions[chatId] ?? EMPTY_UPDATES : EMPTY_UPDATES,
  )
  return useMemo(() => reduceNodeExecutions(updates), [updates])
}