// Copyright (c) 2025 Reliant Labs

/**
 * Wires a builder test run's chat into the canvas.
 *
 * The test run is a real chat, so its node_execution events arrive on the
 * same update stream the Runs viewer reads and land in chatStore, and its
 * execution tree is the one the viewer fetches. This hook (a) asks the stream
 * to follow the test chat and (b) reduces it, with the tree, to what the
 * canvas draws: each step's state, the path taken and each loop's iterations
 * (run/builderRun.ts).
 *
 * The stream follows ONE chat at a time. A test run takes it for as long as it
 * is shown, then hands it back to whichever chat held it before — usually the
 * editor's chat panel. That hand-back is required: ChatContainer re-asserts its
 * subscription only when its chat id or the connection changes, so a slot that
 * isn't returned leaves the panel silent until a reload.
 */

import { useEffect, useMemo } from 'react'
import type { Edge, Node } from '@xyflow/react'
import { useGlobalUpdatesStore } from '../../../store/globalUpdatesStore'
import { useChat } from '../../../hooks/chat-queries'
import { useWorkflowExecutions } from '../../../hooks/useWorkflowExecutions'
import { WorkflowExecutionView } from '../../../gen/reliant/v1/chat_pb'
import { isLiveRunStatus, runStatus, type RunStatusDisplay } from '../../../lib/runStatus'
import { isEntryFlowNodeType } from '../../../lib/workflow-flow'
import type { NodeExecutionUpdate } from '../../../types/streaming'
import type { WorkflowExecution } from '../../Chat/ExecutionSidebar/types'
import { transformWorkflowExecution } from '../../Chat/ExecutionSidebar/transformApiData'
import { useExtendedExecutionStatus } from './useExecutionStatus'
import { nodeExecutionKey, useNodeExecutionStatus } from './useNodeExecutionStatus'
import { deriveBuilderRun, type BuilderRunView } from '../run/builderRun'

export interface BuilderRun {
  chatId: string
  view: BuilderRunView
  /** The run's own status, once the chat has loaded. */
  status: RunStatusDisplay | null
  /**
   * Bumps whenever a step settles, so data read from the run's record (the Run
   * tab, sample values) is fetched again.
   */
  settledVersion: number
}

/**
 * The run's root execution. Until the tree arrives, a bare execution under the
 * chat's id — the root run's workflow id IS the chat id — so the stream alone
 * already paints statuses.
 */
function bareExecution(chatId: string): WorkflowExecution {
  return {
    id: chatId,
    workflowName: '',
    thread: chatId,
    status: 'running',
    createdAt: 0,
    messageCount: 0,
    children: [],
    steps: [],
  }
}

export function useBuilderRun(
  testChatId: string | null,
  nodes: readonly Node[],
  edges: readonly Edge[],
): BuilderRun | null {
  const subscribeToChatDetails = useGlobalUpdatesStore((state) => state.subscribeToChatDetails)
  const unsubscribeFromChatDetails = useGlobalUpdatesStore((state) => state.unsubscribeFromChatDetails)

  useEffect(() => {
    if (!testChatId) return
    const previousChatId = useGlobalUpdatesStore.getState().subscribedChatId
    subscribeToChatDetails(testChatId)
    return () => {
      unsubscribeFromChatDetails(testChatId)
      if (previousChatId && previousChatId !== testChatId) subscribeToChatDetails(previousChatId)
    }
  }, [testChatId, subscribeToChatDetails, unsubscribeFromChatDetails])

  const { allWorkflows } = useWorkflowExecutions(testChatId, WorkflowExecutionView.FULL)
  const execution = useMemo(() => {
    if (!testChatId) return undefined
    const root = allWorkflows.find((wf) => wf.id === testChatId) ?? allWorkflows[0]
    return root ? transformWorkflowExecution(root) : bareExecution(testChatId)
  }, [testChatId, allWorkflows])

  const nodeIds = useMemo(() => nodes.map((n) => n.id), [nodes])
  const { statusMap, loopInfo, latestSequence } = useExtendedExecutionStatus(execution, nodeIds, testChatId)
  const { decidingEventByKey, statusByKey, loopScoped } = useNodeExecutionStatus(testChatId)

  const chat = useChat(testChatId ?? undefined).data
  const status = chat
    ? runStatus({ state: chat.workflowState, stopReason: chat.workflowStopReason, activity: chat.activity })
    : null

  const live = status ? isLiveRunStatus(status) : true
  const awaitingInput = status?.key === 'needs_you'
  const workflowId = execution?.id

  const view = useMemo(() => {
    if (!testChatId || !workflowId) return null
    const decidingEvents: Record<string, NodeExecutionUpdate> = {}
    for (const nodeId of nodeIds) {
      const event = decidingEventByKey[nodeExecutionKey(workflowId, nodeId)]
      if (event) decidingEvents[nodeId] = event
    }
    return deriveBuilderRun({
      nodes,
      edges,
      statusMap,
      loopInfo,
      decidingEvents,
      latestSequence,
      live,
      awaitingInput,
      isEntryType: isEntryFlowNodeType,
    })
  }, [testChatId, workflowId, nodeIds, nodes, edges, statusMap, loopInfo, decidingEventByKey, latestSequence, live, awaitingInput])

  // How many executions of the run have settled, anywhere in it (each loop
  // iteration counts): it only grows as steps finish, and once more when the
  // run ends — not on every event, so the run's record is not refetched for
  // every "started".
  const settledVersion = useMemo(() => {
    const isSettled = (status: string) => status === 'completed' || status === 'failed'
    let settled = Object.values(statusByKey).filter(isSettled).length
    settled += loopScoped.filter((entry) => isSettled(entry.status)).length
    return settled + (live ? 0 : 1)
  }, [statusByKey, loopScoped, live])

  return useMemo(
    () => (testChatId && view ? { chatId: testChatId, view, status, settledVersion } : null),
    [testChatId, view, status, settledVersion],
  )
}
