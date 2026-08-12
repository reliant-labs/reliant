/**
 * The pending mailbox of a thread.
 *
 * A queued message is not in the transcript — it sits in agent_messages until
 * the loop executor drains it at its next step boundary. Nothing streams it to
 * the client, so the only way the UI can know it exists is to ask. This polls
 * while the agent is running and stops the moment it isn't, with one final
 * read on the running→idle edge so a drained queue actually clears instead of
 * leaving stale rows on screen.
 *
 * Forgetting a message has to outlive the requests that predate it. A poll
 * already in flight when the user claims a row answers with a snapshot taken
 * BEFORE the claim, and writing that answer into the cache puts the row back —
 * now sitting in the strip next to the transcript entry it just became. So a
 * forgotten id is not merely filtered out of the current cache value; it is
 * remembered, and every response is filtered through those tombstones on the
 * way in. Ids are uuids and never reused, so a tombstone can only ever suppress
 * the row it was created for.
 */

import { useCallback, useEffect, useMemo, useRef } from "react";
import { useQuery, useQueryClient } from "@tanstack/react-query";
import { chatGrpc, type QueuedAgentMessageView } from "../api/chat-grpc";

const QUEUE_POLL_INTERVAL_MS = 2_500;

export const queuedAgentMessageKeys = {
  all: ["queuedAgentMessages"] as const,
  thread: (chatId?: string, threadId?: string) =>
    [...queuedAgentMessageKeys.all, chatId, threadId] as const,
};

const EMPTY_QUEUE: QueuedAgentMessageView[] = [];

export interface UseQueuedAgentMessagesResult {
  messages: QueuedAgentMessageView[];
  refresh: () => Promise<void>;
  /**
   * Drop one entry for good: it is gone from the strip immediately, and no
   * response — however stale — can bring it back.
   */
  forget: (messageId: string) => void;
}

/**
 * @param isRunning gates polling. Pass the same signal the composer uses for
 * "the agent is busy" (useIsChatRunning) — an idle agent will never drain the
 * mailbox, so polling it is pure noise.
 */
export function useQueuedAgentMessages(
  chatId: string | undefined,
  threadId: string | undefined,
  isRunning: boolean,
): UseQueuedAgentMessagesResult {
  const queryClient = useQueryClient();
  // Memoized so it is a stable dependency: rebuilt fresh each render, the key
  // would re-arm every effect and callback below on every render.
  const queryKey = useMemo(
    () => queuedAgentMessageKeys.thread(chatId, threadId),
    [chatId, threadId],
  );
  const enabled = !!chatId && !!threadId;

  // Forgotten ids, each stamped with the number of requests that had been
  // issued when it was forgotten. The stamp is what makes releasing a
  // tombstone safe: only a request issued AFTER the claim can testify that the
  // server no longer has the row, because only that request was answered from
  // post-claim state. An older request answering late says nothing, even if it
  // happens to omit the id.
  const tombstones = useRef(new Map<string, number>());
  const requestsIssued = useRef(0);

  // Tombstones belong to one thread's mailbox. Carrying them across a thread
  // switch would filter a different queue against ids it never had.
  useEffect(() => {
    tombstones.current = new Map();
    requestsIssued.current = 0;
  }, [chatId, threadId]);

  const { data } = useQuery<QueuedAgentMessageView[]>({
    queryKey,
    queryFn: async () => {
      const issuedAt = ++requestsIssued.current;
      const response = await chatGrpc.listQueuedAgentMessages(chatId!, threadId!);

      const present = new Set(response.messages.map((m) => m.id));
      // A tombstone is a patch over a lagging server, so it expires the moment
      // the server catches up — otherwise every message claimed in a long
      // session would be remembered until the chat closed.
      for (const [id, forgottenAt] of tombstones.current) {
        if (issuedAt > forgottenAt && !present.has(id)) {
          tombstones.current.delete(id);
        }
      }

      return response.messages.filter((m) => !tombstones.current.has(m.id));
    },
    enabled,
    refetchInterval: isRunning ? QUEUE_POLL_INTERVAL_MS : false,
    // A backgrounded tab has no one reading the strip, and the queue is only
    // actionable while the user is looking at it.
    refetchIntervalInBackground: false,
    staleTime: 0,
  });

  // When the agent stops, whatever it drained on the way out is still sitting
  // in our cache. Read once more so the strip reflects the real mailbox rather
  // than the last snapshot taken mid-run.
  const wasRunning = useRef(isRunning);
  useEffect(() => {
    if (wasRunning.current && !isRunning && enabled) {
      void queryClient.invalidateQueries({ queryKey });
    }
    wasRunning.current = isRunning;
  }, [isRunning, enabled, queryKey, queryClient]);

  const forget = useCallback(
    (messageId: string) => {
      tombstones.current.set(messageId, requestsIssued.current);
      queryClient.setQueryData<QueuedAgentMessageView[]>(queryKey, (prev) =>
        prev ? prev.filter((m) => m.id !== messageId) : prev,
      );
    },
    [queryClient, queryKey],
  );

  const refresh = useCallback(async () => {
    if (!enabled) return;
    await queryClient.invalidateQueries({ queryKey });
  }, [queryClient, enabled, queryKey]);

  return { messages: data ?? EMPTY_QUEUE, refresh, forget };
}
