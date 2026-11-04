// Copyright (c) 2025 Reliant Labs

/**
 * Full tool-result content, read on demand.
 *
 * The transcript sends a large tool result as a preview (its block is marked
 * `truncated`, with `original_size`); these fetch the whole result through
 * GetToolResult when the user asks for it — to see it, or to copy it. A plain
 * request/response read the stream never writes, so it lives on the generated
 * entry (`chatServiceQueries.getToolResult`). A stored result never changes,
 * so one fetch per tool call is kept for the session.
 */

import { useQuery } from "@tanstack/react-query";
import { queryClient as appQueryClient } from "../lib/query-client";
import { chatServiceQueries } from "./chat-service-hooks_gen";

const fullToolResultQuery = (chatId: string, toolCallId: string) =>
  chatServiceQueries.getToolResult(
    { chatId, toolCallId },
    {
      // Immutable once written: never refetch what was already read.
      staleTime: Infinity,
      // Full outputs can be large; let an unwatched one go after a while.
      gcTime: 10 * 60 * 1000,
      retry: 1,
    },
  );

/**
 * The full result of a tool call, fetched while `enabled`. A result fetched
 * earlier (by a copy, say) is returned from the cache even when not enabled.
 *
 * Bound to the app's query client, the one fetchFullToolResult fills, so the
 * view and a copy share one cache entry wherever the card is rendered.
 */
export function useFullToolResult(
  chatId: string | undefined,
  toolCallId: string,
  enabled: boolean,
) {
  return useQuery(
    {
      ...fullToolResultQuery(chatId || "", toolCallId),
      enabled: enabled && !!chatId && !!toolCallId,
    },
    appQueryClient,
  );
}

/** The full content of a tool call's result, from the cache or the server. */
export async function fetchFullToolResult(
  chatId: string,
  toolCallId: string,
): Promise<string> {
  const result = await appQueryClient.fetchQuery(fullToolResultQuery(chatId, toolCallId));
  return result.content;
}
