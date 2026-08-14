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
import { useQuery, useQueryClient, type QueryClient } from "@tanstack/react-query";
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
 * One refetch subscription per (client, chat), shared by every mounted reader.
 *
 * A chat's execution tree is read by ChatContainer, every ToolExecutionGroup
 * and every ToolExecution row at once — routinely eight or more instances.
 * When each instance owned its own "workflow_executions" subscription, a
 * single pulse ran eight invalidateQueries calls in one tick, and
 * invalidateQueries defaults to cancelRefetch: true, which ABORTS the fetch
 * already in flight and starts a new one rather than joining it. Eight readers
 * therefore turned one pulse into eight requests within a millisecond or two.
 *
 * Refcounting the subscription means the pulse invalidates once no matter how
 * many readers are mounted. Freshness is unchanged: the same pulse still
 * triggers the same refetch of the same key at the same moment.
 */
type SubscriptionEntry = { readers: number; unsubscribe: () => void };

const refetchSubscriptions = new WeakMap<
  QueryClient,
  Map<string, SubscriptionEntry>
>();

function acquireRefetchSubscription(
  queryClient: QueryClient,
  chatId: string,
): () => void {
  let perChat = refetchSubscriptions.get(queryClient);
  if (!perChat) {
    perChat = new Map();
    refetchSubscriptions.set(queryClient, perChat);
  }

  let entry = perChat.get(chatId);
  if (!entry) {
    entry = {
      readers: 0,
      unsubscribe: subscribeToRefetch("workflow_executions", () => {
        queryClient.invalidateQueries(
          { queryKey: chatDetailKeys.workflowExecutions(chatId) },
          // SECOND argument. invalidateQueries is
          // (filters, options) — cancelRefetch is an InvalidateOptions field,
          // not a filter, so passing it in the first object was silently
          // dropped by the runtime and rejected by the types.
          //
          // Join the fetch already in flight instead of aborting and
          // reissuing it. Without this, concurrent invalidations of the same
          // key each start their own request.
          { cancelRefetch: false },
        );
      }),
    };
    perChat.set(chatId, entry);
  }
  entry.readers += 1;

  let released = false;
  return () => {
    if (released) return;
    released = true;
    entry.readers -= 1;
    if (entry.readers === 0) {
      entry.unsubscribe();
      perChat.delete(chatId);
    }
  };
}

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
  //
  // The subscription is shared across every reader of this chat — see
  // acquireRefetchSubscription — so one pulse means one request, not one per
  // mounted component.
  useEffect(() => {
    if (!chatId) return;
    return acquireRefetchSubscription(queryClient, chatId);
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
