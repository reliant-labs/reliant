// Copyright (c) 2025 Reliant Labs

/**
 * Wires a builder test run's chat into the canvas.
 *
 * The test run is a real chat, so its node_execution events arrive on the
 * same update stream the Runs viewer reads and land in chatStore. This hook
 * (a) asks the stream to follow the test chat and (b) reduces its events to a
 * status per node id, which the builder paints onto its own nodes.
 *
 * The stream follows ONE chat at a time. A test run takes it for as long as it
 * is shown, then hands it back to whichever chat held it before — usually the
 * editor's chat panel. That hand-back is required: ChatContainer re-asserts its
 * subscription only when its chat id or the connection changes, so a slot that
 * isn't returned leaves the panel silent until a reload.
 */

import { useEffect, useMemo } from 'react'
import type { Node } from '@xyflow/react'
import { useGlobalUpdatesStore } from '../../../store/globalUpdatesStore'
import { nodeExecutionKey, useNodeExecutionStatus, type StreamNodeStatus } from './useNodeExecutionStatus'

export function useBuilderTestRun(
  testChatId: string | null,
  nodeIds: readonly string[],
): Record<string, StreamNodeStatus> {
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

  // The root run's workflow id is the chat id, so a node's identity in the
  // stream is `${chatId}:${nodeId}`.
  const { statusByKey } = useNodeExecutionStatus(testChatId)
  return useMemo(() => {
    const byNodeId: Record<string, StreamNodeStatus> = {}
    if (!testChatId) return byNodeId
    for (const nodeId of nodeIds) {
      const status = statusByKey[nodeExecutionKey(testChatId, nodeId)]
      if (status) byNodeId[nodeId] = status
    }
    return byNodeId
  }, [testChatId, nodeIds, statusByKey])
}

/** The canvas nodes with a test run's statuses painted on; identity-stable when there is nothing to paint. */
export function withTestRunStatus<T extends Node>(
  nodes: T[],
  statusByNodeId: Record<string, StreamNodeStatus>,
): T[] {
  if (Object.keys(statusByNodeId).length === 0) return nodes
  return nodes.map((node) => {
    const status = statusByNodeId[node.id]
    return status ? { ...node, data: { ...node.data, executionStatus: status } } : node
  })
}
