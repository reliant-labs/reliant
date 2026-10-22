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

  // A loop runs its body inline, and its step rows are written as each
  // activity FINISHES — so every row says completed (or failed), even while
  // the loop is halfway through iteration 2. With no stream to say what is
  // running, those rows used to read as a finished loop: "Done".
  describe('tree fallback: a loop is running until the run moves past it', () => {
    function loopRow(stepId: string, iteration: number, createdAt: number, status: StepExecution['status'] = 'completed') {
      return step({ stepId, loopNodeId: 'attempt', loopIteration: iteration, status, createdAt })
    }
    const nodes = ['plan', 'attempt', 'ship']

    it('reads a loop in iteration 2, whose rows all say completed, as running', () => {
      const exec = execution({
        status: 'running',
        steps: [
          step({ stepId: 'plan', createdAt: 5 }),
          loopRow('implement', 0, 10),
          loopRow('lint', 0, 20),
          loopRow('implement', 1, 30),
        ],
      })

      const { statusMap } = render(exec, nodes, null)

      expect(statusMap['attempt']).toBe('running')
      expect(statusMap['plan']).toBe('completed')
      expect(statusMap['ship']).toBeUndefined()
    })

    it('reads a loop between iterations (the last one fully finished) as running', () => {
      const exec = execution({
        status: 'running',
        steps: [step({ stepId: 'plan', createdAt: 5 }), loopRow('implement', 0, 10), loopRow('lint', 0, 20)],
      })

      expect(render(exec, nodes, null).statusMap['attempt']).toBe('running')
    })

    it('does not fail a running loop over a check that failed inside it', () => {
      // A failed lint is the normal shape of a review iteration; the loop
      // goes round again.
      const exec = execution({
        status: 'running',
        steps: [loopRow('implement', 0, 10), loopRow('lint', 0, 20, 'failed')],
      })

      expect(render(exec, nodes, null).statusMap['attempt']).toBe('running')
    })

    it('reads a loop as done once another node has run after it', () => {
      const exec = execution({
        status: 'running',
        steps: [loopRow('implement', 0, 10), loopRow('lint', 0, 20), step({ stepId: 'ship', createdAt: 30 })],
      })

      const { statusMap } = render(exec, nodes, null)

      expect(statusMap['attempt']).toBe('completed')
      expect(statusMap['ship']).toBe('completed')
    })

    it('reads a loop as done once a child workflow of a later node is spawned', () => {
      const exec = execution({
        status: 'running',
        steps: [loopRow('implement', 0, 10), loopRow('lint', 0, 20)],
        children: [execution({ id: 'wf-ship', spawnedByNodeId: 'ship', status: 'running', createdAt: 30 })],
      })

      const { statusMap } = render(exec, nodes, null)

      expect(statusMap['attempt']).toBe('completed')
      expect(statusMap['ship']).toBe('running')
    })

    it('does not count a row from a scope nested inside the loop as the run moving on', () => {
      // An agent inside the loop body runs its own inner loop; its rows name
      // that inner loop, which is not a node of this workflow, so they say
      // nothing about which root node ran.
      const exec = execution({
        status: 'running',
        steps: [
          loopRow('implement', 0, 10),
          step({ stepId: 'call_llm', loopNodeId: 'agent_loop', loopIteration: 3, createdAt: 40 }),
        ],
      })

      expect(render(exec, nodes, null).statusMap['attempt']).toBe('running')
    })

    it('reads the loop of a finished run from its rows', () => {
      const exec = execution({
        status: 'completed',
        steps: [loopRow('implement', 0, 10), loopRow('lint', 0, 20)],
      })

      expect(render(exec, nodes, null).statusMap['attempt']).toBe('completed')
    })

    it('leaves a loop to the stream once the stream has spoken for the run', () => {
      // The stream knows the run is in "ship" now and that nothing inside the
      // loop is busy, so the loop is done — even though no row of "ship"
      // exists yet to say so.
      seedStream([nodeEvent('ship', NodeExecutionEventType.STARTED, 50)])
      const exec = execution({
        status: 'running',
        steps: [loopRow('implement', 0, 10), loopRow('lint', 0, 20)],
      })

      const { statusMap } = render(exec, nodes, CHAT_ID)

      expect(statusMap['attempt']).toBe('completed')
      expect(statusMap['ship']).toBe('running')
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
