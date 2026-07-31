import { describe, it, expect, beforeEach } from 'vitest'
import { renderHook } from '@testing-library/react'
import {
  useExtendedExecutionStatus,
  type ExecutionStatusResult,
} from './useExecutionStatus'
import { nodeExecutionKey } from './useNodeExecutionStatus'
import { useChatStore } from '../../../store/chatStore'
import type {
  WorkflowExecution,
  StepExecution,
} from '../../Chat/ExecutionSidebar/types'
import type { NodeExecutionUpdate } from '../../../types/streaming'
import {
  NodeExecutionEventType,
  NodeExecutionStatus as ProtoNodeExecutionStatus,
} from '../../../gen/reliant/v1/streaming_pb'

// ---------------------------------------------------------------------------
// Characterization tests for useExecutionStatus (Phase 2).
//
// Post-Phase-2 SOURCE-OF-TRUTH SPLIT:
//  - Node STATUS (running/completed/failed) is authoritative from the
//    node_execution STREAM (chatStore.nodeExecutions, read via chatId). The old
//    position inference ("the node after the last completed one is running") is
//    DELETED — a running node is now known only because the stream said so.
//  - Loop STRUCTURE (currentIteration, iterationStatuses) stays tree-derived.
//  - When there is NO stream event for a node yet, status falls back to a
//    FACTUAL tree derivation from that node's own executions (its direct step,
//    spawned loop steps, or child workflow) — never position inference.
//
// The OUTPUT CONTRACT consumers rely on (statusMap + loopInfo shape) is
// unchanged; only the SOURCE of the running/terminal status moved.
// ---------------------------------------------------------------------------

const WF_ID = 'wf-root'
const CHAT_ID = 'chat-exec-status'

let stepUid = 0
function step(overrides: Partial<StepExecution> = {}): StepExecution {
  stepUid += 1
  return {
    id: `step-${stepUid}`,
    stepId: overrides.stepId ?? `node-${stepUid}`,
    activityName: 'V2_CallLLM',
    status: 'completed',
    createdAt: stepUid,
    ...overrides,
  }
}

function execution(overrides: Partial<WorkflowExecution> = {}): WorkflowExecution {
  return {
    id: WF_ID,
    workflowName: 'builtin://demo',
    thread: WF_ID,
    status: 'running',
    createdAt: 0,
    messageCount: 0,
    children: [],
    steps: [],
    ...overrides,
  }
}

function nodeEvent(
  nodeId: string,
  eventType: NodeExecutionEventType,
  seq: number,
): NodeExecutionUpdate {
  return {
    update_type: 'node_execution',
    event_type: eventType,
    node_id: nodeId,
    node_type: 'action',
    status: ProtoNodeExecutionStatus.RUNNING,
    workflow_id: WF_ID,
    chat_id: CHAT_ID,
    sequence_number: seq,
  }
}

/** Seed chatStore.nodeExecutions for CHAT_ID with the given stream events. */
function seedStream(events: NodeExecutionUpdate[]) {
  useChatStore.setState({
    nodeExecutions: { [CHAT_ID]: events },
  } as never)
}

function render(
  exec: WorkflowExecution | undefined,
  nodeIds: string[],
  chatId: string | null = CHAT_ID,
): ExecutionStatusResult {
  const { result } = renderHook(() =>
    useExtendedExecutionStatus(exec, nodeIds, chatId),
  )
  return result.current
}

beforeEach(() => {
  stepUid = 0
  seedStream([])
})

describe('useExecutionStatus (characterization — Phase 2 stream source)', () => {
  it('returns an empty contract when there is no execution', () => {
    const { statusMap, loopInfo } = render(undefined, ['a', 'b'])
    expect(statusMap).toEqual({})
    expect(loopInfo).toEqual({})
  })

  it('linear: completed node then running node — sourced from the stream', () => {
    // "a" completed, "b" running — both known from the node_execution stream,
    // NOT from position inference. "c" has no event, so it has no status.
    seedStream([
      nodeEvent('a', NodeExecutionEventType.STARTED, 1),
      nodeEvent('a', NodeExecutionEventType.COMPLETED, 2),
      nodeEvent('b', NodeExecutionEventType.STARTED, 3),
    ])
    const exec = execution({ status: 'running' })

    const { statusMap } = render(exec, ['a', 'b', 'c'])

    expect(statusMap['workflow']).toBe('completed')
    expect(statusMap['a']).toBe('completed')
    expect(statusMap['b']).toBe('running')
    expect(statusMap['c']).toBeUndefined()
  })

  it('failed: a failed node comes from the stream', () => {
    seedStream([
      nodeEvent('a', NodeExecutionEventType.COMPLETED, 1),
      nodeEvent('b', NodeExecutionEventType.FAILED, 2),
    ])
    const exec = execution({ status: 'failed' })

    const { statusMap } = render(exec, ['a', 'b', 'c'])

    expect(statusMap['a']).toBe('completed')
    expect(statusMap['b']).toBe('failed')
    expect(statusMap['c']).toBeUndefined()
  })

  it('loop: currentIteration + iterationStatuses stay tree-derived (structure)', () => {
    // Loop iteration grouping is NOT carried by the stream — it comes from the
    // tree's spawned steps. The loop node's own status still comes from the
    // stream when present; here we assert the STRUCTURE (loopInfo) is intact.
    seedStream([nodeEvent('loop1', NodeExecutionEventType.STARTED, 1)])
    const exec = execution({
      status: 'running',
      steps: [
        step({
          stepId: 'loop1-body',
          loopNodeId: 'loop1',
          loopIteration: 0,
          status: 'completed',
          createdAt: 10,
        }),
        step({
          stepId: 'loop1-body',
          loopNodeId: 'loop1',
          loopIteration: 1,
          status: 'running',
          createdAt: 20,
        }),
      ],
    })

    const { statusMap, loopInfo } = render(exec, ['loop1'])

    expect(statusMap['loop1']).toBe('running')

    const info = loopInfo['loop1']
    expect(info).toBeDefined()
    expect(info.currentIteration).toBe(1)
    expect(info.completedIterations).toBe(1)
    expect(info.iterationStatuses).toEqual(['completed', 'running'])
  })

  it('terminal guard end-to-end: a late started does not un-complete a node', () => {
    seedStream([
      nodeEvent('a', NodeExecutionEventType.STARTED, 1),
      nodeEvent('a', NodeExecutionEventType.COMPLETED, 2),
      nodeEvent('a', NodeExecutionEventType.STARTED, 3), // stale
    ])
    const { statusMap } = render(execution({ status: 'running' }), ['a'])
    expect(statusMap['a']).toBe('completed')
  })

  describe('tree fallback (no stream events)', () => {
    it('uses a node\'s own direct step when the stream is silent', () => {
      // No stream events at all → fall back to the node\'s factual step record.
      const exec = execution({
        status: 'running',
        steps: [step({ stepId: 'a', status: 'completed', createdAt: 10 })],
      })

      const { statusMap } = render(exec, ['a', 'b'], CHAT_ID)

      expect(statusMap['a']).toBe('completed')
      // "b" has no step and no stream event → NO position inference → undefined.
      expect(statusMap['b']).toBeUndefined()
    })

    it('resolves a suffixed step id to its node in the fallback (linkage)', () => {
      const exec = execution({
        status: 'running',
        steps: [step({ stepId: 'call_llm-save', status: 'completed', createdAt: 10 })],
      })

      const { statusMap } = render(exec, ['call_llm', 'next'], CHAT_ID)

      expect(statusMap['call_llm']).toBe('completed')
      // "next" is NOT inferred running anymore — the deleted position inference.
      expect(statusMap['next']).toBeUndefined()
    })

    it('the stream overrides the tree fallback when both are present', () => {
      // Tree says "a" completed via its step; stream says "a" is running.
      // Stream wins (it is authoritative for status).
      seedStream([nodeEvent('a', NodeExecutionEventType.STARTED, 1)])
      const exec = execution({
        status: 'running',
        steps: [step({ stepId: 'a', status: 'completed', createdAt: 10 })],
      })

      const { statusMap } = render(exec, ['a'], CHAT_ID)
      expect(statusMap['a']).toBe('running')
    })
  })
})

// Reference the key helper so the module import is exercised and the alignment
// (execution.id === stream workflow_id) is documented in-test.
describe('key alignment', () => {
  it('uses `${execution.id}:${nodeId}` as the stream identity', () => {
    expect(nodeExecutionKey(WF_ID, 'a')).toBe(`${WF_ID}:a`)
  })
})
