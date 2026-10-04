// Copyright (c) 2025 Reliant Labs

import { useQuery } from "@tanstack/react-query";

import { api } from "@/api/client";
import { chatKeys } from "@/hooks/chat-queries";
import { isLiveRunStatus, runStatus, type RunStatusDisplay } from "@/lib/runStatus";

/** How often a still-live launched run is re-read while its history is open. */
const LIVE_RUN_REFETCH_MS = 15_000;

/**
 * The status of the run a launched trigger event started, read from that
 * chat (GetChat carries the root run's state, stop reason and activity).
 *
 * One read per launched event, so it belongs on the automation's own page,
 * whose history is bounded by the events query, and not on the cross-trigger
 * list. The cache entry is the chat's detail entry, shared with every other
 * reader of that chat.
 */
export function useLaunchedRunStatus(chatId?: string): {
  status?: RunStatusDisplay;
  /** The chat could not be read (deleted since, or the read failed). */
  unavailable: boolean;
} {
  const { data: chat, isError } = useQuery({
    queryKey: chatKeys.detail(chatId),
    queryFn: () => api.chatsV2.get(chatId!),
    enabled: !!chatId,
    refetchInterval: (query) => {
      const current = query.state.data;
      if (!current) return false;
      const status = runStatus({
        state: current.workflowState,
        stopReason: current.workflowStopReason,
        activity: current.activity,
      });
      return isLiveRunStatus(status) ? LIVE_RUN_REFETCH_MS : false;
    },
  });
  if (!chat) return { unavailable: isError };
  return {
    status: runStatus({
      state: chat.workflowState,
      stopReason: chat.workflowStopReason,
      activity: chat.activity,
    }),
    unavailable: false,
  };
}
