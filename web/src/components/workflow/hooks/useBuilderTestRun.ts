// Copyright (c) 2025 Reliant Labs

/**
 * Wires a builder test run's chat into the canvas.
 *
 * The test run is a real chat, so its node_execution events arrive on the
 * same update stream the Runs viewer reads and land in chatStore. This hook
 * (a) asks the stream to follow the test chat and (b) reduces its events to a
 * status per node id, which the builder paints onto its own nodes.
 *
 * The stream follows ONE chat at a time and the builder assistant holds that
 * slot for its own chat. A test run takes it for as long as it is shown and
 * hands it back afterwards, so the assistant is not left silent.
 */

import { useEffect, useMemo } from 'react'
import type { Node } from '@xyflow/react'
import { useGlobalUpdatesStore } from '../../../store/globalUpdatesStore'
import { nodeExecutionKey, useNodeExecutionStatus, type StreamNodeStatus } from './useNodeExecutionStatus'

export function useBuilderTestRun(
  testChatId: string | null,
  nodeIds: readonly string[],
  builderChatId?: string,
): Record<string, StreamNodeStatus> {
  const subscribeToChatDetails = useGlobalUpdatesStore((state) => state.subscribeToChatDetails)
  const unsubscribeFromChatDetails = useGlobalUpdatesStore((state) => state.unsubscribeFromChatDetails)

  useEffect(() => {
    if (!testChatId) return
    subscribeToChatDetails(testChatId)
    return () => {
      unsubscribeFromChatDetails(testChatId)
      if (builderChatId) subscribeToChatDetails(builderChatId)
    }
  }, [testChatId, builderChatId, subscribeToChatDetails, unsubscribeFromChatDetails])

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
