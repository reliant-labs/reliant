import { describe, it, expect } from 'vitest'
import {
  reduceNodeExecutions,
  nodeExecutionKey,
} from './useNodeExecutionStatus'
import type { NodeExecutionUpdate } from '../../../types/streaming'
import {
  NodeExecutionEventType,
  NodeExecutionStatus as ProtoNodeExecutionStatus,
} from '../../../gen/reliant/v1/streaming_pb'

// ---------------------------------------------------------------------------
// Characterization tests for the stream-derived node status reducer (STEP 2).
//
// The reducer folds chatStore.nodeExecutions[chatId] into an authoritative
// status map keyed by `${workflowId}:${nodeId}`. It must:
//  - map started/progress -> running, completed -> completed, failed -> failed
//  - handle BOTH runtime shapes: numeric proto enums (live path) and string
//    event_type (persisted / snapshot path)
//  - never let a late 'running' un-complete or un-fail a terminal node
//  - be order-insensitive (precedence by terminal-ness then sequence_number)
//  - keep parallel nodes independent
// ---------------------------------------------------------------------------

const WF = 'wf-1'

/** Live-path event: numeric proto enums. */
function liveEvent(
  nodeId: string,
  eventType: NodeExecutionEventType,
  seq: number,
  overrides: Partial<NodeExecutionUpdate> = {},
): NodeExecutionUpdate {
  return {
    update_type: 'node_execution',
    event_type: eventType,
    node_id: nodeId,
    node_type: 'action',
    status: ProtoNodeExecutionStatus.RUNNING,
    workflow_id: WF,
    chat_id: 'chat-1',
    sequence_number: seq,
    ...overrides,
  }
}

/** Persisted/snapshot-path event: string event_type, numeric status. */
function persistedEvent(
  nodeId: string,
  eventType: 'started' | 'progress' | 'completed' | 'failed' | 'cancelled',
  seq: number,
  overrides: Partial<NodeExecutionUpdate> = {},
): NodeExecutionUpdate {
  const statusMap: Record<string, ProtoNodeExecutionStatus> = {
    started: ProtoNodeExecutionStatus.RUNNING,
    progress: ProtoNodeExecutionStatus.RUNNING,
    completed: ProtoNodeExecutionStatus.COMPLETED,
    failed: ProtoNodeExecutionStatus.FAILED,
    cancelled: ProtoNodeExecutionStatus.CANCELLED,
  }
  return {
    update_type: 'node_execution',
    // Cast: the persisted path really carries a string here even though the
    // TS type declares the numeric enum. This test pins that runtime reality.
    event_type: eventType as unknown as NodeExecutionEventType,
    node_id: nodeId,
    node_type: 'action',
    status: statusMap[eventType],
    workflow_id: WF,
    chat_id: 'chat-1',
    sequence_number: seq,
    ...overrides,
  }
}

describe('reduceNodeExecutions (stream-derived node status)', () => {
  it('returns empty maps for no events', () => {
    expect(reduceNodeExecutions([])).toEqual({
      statusByKey: {},
      iterationByKey: {},
    })
  })

  it('live path: started -> running, completed -> completed, failed -> failed', () => {
    const { statusByKey } = reduceNodeExecutions([
      liveEvent('a', NodeExecutionEventType.STARTED, 1),
      liveEvent('a', NodeExecutionEventType.COMPLETED, 2),
      liveEvent('b', NodeExecutionEventType.STARTED, 3),
      liveEvent('c', NodeExecutionEventType.STARTED, 4),
      liveEvent('c', NodeExecutionEventType.FAILED, 5),
    ])

    expect(statusByKey[nodeExecutionKey(WF, 'a')]).toBe('completed')
    expect(statusByKey[nodeExecutionKey(WF, 'b')]).toBe('running')
    expect(statusByKey[nodeExecutionKey(WF, 'c')]).toBe('failed')
  })

  it('persisted path: string event_type maps the same way', () => {
    const { statusByKey } = reduceNodeExecutions([
      persistedEvent('a', 'started', 1),
      persistedEvent('a', 'completed', 2),
      persistedEvent('b', 'failed', 3),
    ])

    expect(statusByKey[nodeExecutionKey(WF, 'a')]).toBe('completed')
    expect(statusByKey[nodeExecutionKey(WF, 'b')]).toBe('failed')
  })

  it('terminal guard: a late started does NOT un-complete a node', () => {
    const { statusByKey } = reduceNodeExecutions([
      liveEvent('a', NodeExecutionEventType.STARTED, 1),
      liveEvent('a', NodeExecutionEventType.COMPLETED, 2),
      // Stale 'started' racing in AFTER completion (higher seq) must be ignored.
      liveEvent('a', NodeExecutionEventType.STARTED, 3),
    ])

    expect(statusByKey[nodeExecutionKey(WF, 'a')]).toBe('completed')
  })

  it('terminal guard: a late started does NOT un-fail a node', () => {
    const { statusByKey } = reduceNodeExecutions([
      liveEvent('a', NodeExecutionEventType.FAILED, 5),
      liveEvent('a', NodeExecutionEventType.STARTED, 9),
    ])

    expect(statusByKey[nodeExecutionKey(WF, 'a')]).toBe('failed')
  })

  it('order-insensitive: events arriving out of order still resolve terminal', () => {
    // Same events as the terminal-guard case but shuffled in array order.
    const { statusByKey } = reduceNodeExecutions([
      liveEvent('a', NodeExecutionEventType.STARTED, 3), // late, arrives first
      liveEvent('a', NodeExecutionEventType.COMPLETED, 2),
      liveEvent('a', NodeExecutionEventType.STARTED, 1),
    ])

    expect(statusByKey[nodeExecutionKey(WF, 'a')]).toBe('completed')
  })

  it('terminal correction: completed then failed (higher seq) wins', () => {
    const { statusByKey } = reduceNodeExecutions([
      liveEvent('a', NodeExecutionEventType.COMPLETED, 2),
      liveEvent('a', NodeExecutionEventType.FAILED, 4),
    ])

    expect(statusByKey[nodeExecutionKey(WF, 'a')]).toBe('failed')
  })

  it('parallel nodes are independent', () => {
    const { statusByKey } = reduceNodeExecutions([
      liveEvent('a', NodeExecutionEventType.STARTED, 1),
      liveEvent('b', NodeExecutionEventType.STARTED, 2),
      liveEvent('a', NodeExecutionEventType.COMPLETED, 3),
      // b stays running while a completes.
    ])

    expect(statusByKey[nodeExecutionKey(WF, 'a')]).toBe('completed')
    expect(statusByKey[nodeExecutionKey(WF, 'b')]).toBe('running')
  })

  it('nodes in different workflows do not collide', () => {
    const { statusByKey } = reduceNodeExecutions([
      liveEvent('a', NodeExecutionEventType.COMPLETED, 1),
      liveEvent('a', NodeExecutionEventType.STARTED, 2, { workflow_id: 'wf-2' }),
    ])

    expect(statusByKey[nodeExecutionKey(WF, 'a')]).toBe('completed')
    expect(statusByKey[nodeExecutionKey('wf-2', 'a')]).toBe('running')
  })

  it('carries iteration info from the latest event that has it', () => {
    const { iterationByKey } = reduceNodeExecutions([
      liveEvent('loop', NodeExecutionEventType.STARTED, 1, {
        iteration: 0,
        max_iterations: 3,
      }),
      liveEvent('loop', NodeExecutionEventType.PROGRESS, 2, {
        iteration: 1,
        max_iterations: 3,
      }),
    ])

    expect(iterationByKey[nodeExecutionKey(WF, 'loop')]).toEqual({
      iteration: 1,
      maxIterations: 3,
    })
  })
})
