/**
 * Hook to fetch the workflow execution tree for a chat.
 *
 * The execution tree is homed in React Query under the generated
 * GetWorkflowExecutions key (chatServiceKeys). Multiple components calling
 * useWorkflowExecutions(chatId) for the same chatId share a single query cache
 * entry (and therefore a single in-flight fetch) instead of issuing duplicate
 * API calls. It is a pull read — the stream only says it changed — so the
 * entry is always some request's answer, and a generated ChatService
 * mutation's `chatServiceKeys.all` invalidation refetching it is correct.
 *
 * Instead of polling, this hook subscribes to "workflow_executions" refetch
 * events from the chat stream and invalidates the query when one arrives. The
 * backend emits these events when workflow status changes (start, complete,
 * fail, cancel).
 */

import { useEffect } from "react";
import { useQueryClient, type QueryClient } from "@tanstack/react-query";
import type { WorkflowExecutionData } from "../types/chat";
import {
  WorkflowExecutionView,
  type GetWorkflowExecutionsResponse,
} from "../gen/reliant/v1/chat_pb";
import { subscribeToRefetch } from "../store/refetchStore";
import { chatServiceKeys, useGetWorkflowExecutions } from "./chat-service-hooks_gen";

interface UseWorkflowExecutionsResult {
  /** The most recent/active workflow (for backwards compat) */
  data: WorkflowExecutionData | null;
  /** All root workflows for this chat, sorted newest first */
  allWorkflows: WorkflowExecutionData[];
  isLoading: boolean;
  error: Error | null;
  refetch: () => Promise<void>;
}

const EMPTY_ALL: WorkflowExecutionData[] = [];

/**
 * Every cached view (BASIC, FULL) of one chat's execution tree. A generated
 * key ends in the request's protojson, and React Query matches an object in a
 * key partially, so a request naming only the chat matches the entry of every
 * view of it. `chatId` must be non-empty: an empty one serializes to `{}`,
 * which would match every chat's tree.
 */
export function workflowExecutionsOfChat(chatId: string) {
  return chatServiceKeys.getWorkflowExecutions({ chatId });
}

interface WorkflowExecutionTree {
  latest: WorkflowExecutionData | null;
  all: WorkflowExecutionData[];
}

/**
 * The response as the hook's readers use it. No copy: a proto
 * WorkflowExecution is a WorkflowExecutionData (the frontend type only makes
 * `$typeName` optional), and a FULL tree can be tens of megabytes, read by
 * every mounted observer's `select`.
 */
function toTree(response: GetWorkflowExecutionsResponse): WorkflowExecutionTree {
  return { latest: response.rootWorkflow ?? null, all: response.allRootWorkflows };
}

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
          // The chat's key PREFIX, so one pulse refreshes every view of the
          // tree that is cached (BASIC for the timeline, FULL while the viewer
          // is open) — and React Query refetches only the ones still mounted.
          { queryKey: workflowExecutionsOfChat(chatId) },
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
 * Multiple hook instances for the same (chatId, view) share a single query
 * cache entry.
 *
 * `view` defaults to BASIC, which is what the chat timeline and everything that
 * only walks the tree need: the full workflow tree, but only the handful of
 * steps the timeline draws. Ask for FULL only from a surface that summarises
 * every step — the workflow viewer — and only while it is mounted. For the
 * worst real chat FULL is 93,568 steps and 56.8 MB; BASIC is six steps.
 */
export function useWorkflowExecutions(
  chatId: string | null,
  view: WorkflowExecutionView = WorkflowExecutionView.BASIC,
): UseWorkflowExecutionsResult {
  const queryClient = useQueryClient();

  const query = useGetWorkflowExecutions({ chatId: chatId ?? "", view }, {
    enabled: !!chatId,
    select: toTree,
    // The tree must outlive an unmount exactly as long as the messages it
    // classifies. The messages cache is gcTime: Infinity (message-queries.ts),
    // so a chat switched back to renders its transcript on the first frame —
    // and the tree is the only thing that then says which thread is a spawned
    // sub-agent, because the chat's live thread records were cleared on
    // unsubscribe. With the 5-minute default, any chat left for longer
    // rendered its cached messages against NO tree: InterleavedTimeline could
    // not classify a single spawn thread and dropped all of their messages
    // until the refetch landed. Both caches are released together by
    // evictChat (archive/delete) and queryClient.clear() (logout).
    gcTime: Infinity,
    // Freshness is the stream's job, not a timer's. A change to the tree is
    // announced as a "workflow_executions" refetch, which is a PERSISTED chat
    // update: delivered live while the chat is subscribed, and replayed from
    // the chat's cursor (or carried in its snapshot) when it is reopened. So
    // the cached tree is never silently behind, and the 30s default only
    // re-fetched it on every chat open past that window, for no new answer.
    staleTime: Infinity,
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

  return {
    data: latest,
    allWorkflows: all,
    isLoading: query.isLoading,
    error: query.error,
    refetch: async () => {
      await query.refetch();
    },
  };
}
