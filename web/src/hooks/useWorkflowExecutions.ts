/**
 * Hook to fetch the workflow execution tree for a chat.
 *
 * The execution tree is homed in React Query, keyed by chatId. Multiple
 * components calling useWorkflowExecutions(chatId) for the same chatId share a
 * single query cache entry (and therefore a single in-flight fetch) instead of
 * issuing duplicate API calls.
 *
 * Instead of polling, this hook subscribes to "workflow_executions" refetch
 * events from the chat stream and invalidates the query when one arrives. The
 * backend emits these events when workflow status changes (start, complete,
 * fail, cancel).
 */

import { useEffect } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { chatGrpc, type WorkflowExecutionData } from "../api/chat-grpc";
import { ChatWorkflowStatus } from "../gen/reliant/v1/chat_pb";
import { subscribeToRefetch } from "../store/refetchStore";
import { chatDetailKeys } from "./chat-detail-keys";

interface UseWorkflowExecutionsResult {
  /** The most recent/active workflow (for backwards compat) */
  data: WorkflowExecutionData | null;
  /** All root workflows for this chat, sorted newest first */
  allWorkflows: WorkflowExecutionData[];
  /** Whether any workflow is currently running */
  hasRunningWorkflow: boolean;
  isLoading: boolean;
  error: Error | null;
  refetch: () => Promise<void>;
}

const EMPTY_ALL: WorkflowExecutionData[] = [];

/**
 * Fetches and maintains the workflow execution tree for a chat.
 * Automatically updates when the backend emits workflow_executions refetch events.
 *
 * Multiple hook instances for the same chatId share a single query cache entry.
 */
export function useWorkflowExecutions(
  chatId: string | null,
): UseWorkflowExecutionsResult {
  const queryClient = useQueryClient();

  const query = useQuery({
    queryKey: chatDetailKeys.workflowExecutions(chatId ?? ""),
    queryFn: () => chatGrpc.getWorkflowExecutions(chatId!),
    enabled: !!chatId,
  });

  // Behavior-preserving bridge: the backend drives freshness via
  // "workflow_executions" refetch pulses on the chat stream. Invalidate the
  // query so React Query refetches in place. (Phase 2 will switch this to
  // setQueryData patching from the stream; for now we keep invalidate.)
  useEffect(() => {
    if (!chatId) return;
    const unsubscribe = subscribeToRefetch("workflow_executions", () => {
      queryClient.invalidateQueries({
        queryKey: chatDetailKeys.workflowExecutions(chatId),
      });
    });
    return unsubscribe;
  }, [chatId, queryClient]);

  const all = query.data?.all ?? EMPTY_ALL;
  const latest = query.data?.latest ?? null;

  const hasRunningWorkflow = all.some(
    (wf) => wf.status === ChatWorkflowStatus.RUNNING,
  );

  return {
    data: latest,
    allWorkflows: all,
    hasRunningWorkflow,
    isLoading: query.isLoading,
    error: query.error as Error | null,
    refetch: async () => {
      await query.refetch();
    },
  };
}
