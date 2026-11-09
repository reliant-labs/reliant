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
 *
 * A result a stub read left out (elided) names its block: its full content is
 * read by block id instead (GetContentBlockBodies with `full`), which shares
 * the body cache of hooks/content-block-bodies.ts. Only an elided result takes
 * that path: its server has the rpc, while a truncated result from an older
 * server keeps GetToolResult.
 */

import { useQuery } from "@tanstack/react-query";
import { queryClient as appQueryClient } from "../lib/query-client";
import { chatServiceQueries } from "./chat-service-hooks_gen";
import {
  contentBlockBodyQueryKey,
  fetchContentBlockBodies,
  ContentBlockBodyNotFoundError,
  type ContentBlockBody,
} from "./content-block-bodies";

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
  /** The result's block, when the transcript elided it: read by block id. */
  elidedBlockId?: string,
) {
  const byCall = useQuery(
    {
      ...fullToolResultQuery(chatId || "", toolCallId),
      enabled: !elidedBlockId && enabled && !!chatId && !!toolCallId,
    },
    appQueryClient,
  );
  const byBlock = useQuery(
    {
      queryKey: [...contentBlockBodyQueryKey(chatId || "", elidedBlockId || "", true), "content"] as const,
      queryFn: () => fullElidedResult(chatId || "", elidedBlockId || ""),
      enabled: !!elidedBlockId && enabled && !!chatId,
      staleTime: Infinity,
      gcTime: 10 * 60 * 1000,
      retry: 1,
    },
    appQueryClient,
  );
  return elidedBlockId ? byBlock : byCall;
}

async function fullElidedResult(chatId: string, blockId: string): Promise<{ content: string }> {
  const { bodies } = await fetchContentBlockBodies(chatId, [blockId], { full: true });
  const body = bodies[blockId];
  if (!body) throw new ContentBlockBodyNotFoundError(blockId);
  return { content: body.content ?? "" };
}

/**
 * The full content of a tool call's result, from the cache or the server.
 * `elidedBlockId` names the result's block when the transcript elided it.
 */
export async function fetchFullToolResult(
  chatId: string,
  toolCallId: string,
  elidedBlockId?: string,
): Promise<string> {
  if (elidedBlockId) return (await fullElidedResult(chatId, elidedBlockId)).content;
  const result = await appQueryClient.fetchQuery(fullToolResultQuery(chatId, toolCallId));
  return result.content;
}

export interface FullToolBodies {
  /** The whole bodies of the elided blocks asked for, by block id. */
  bodies: Record<string, ContentBlockBody>;
  /** The whole content of the truncated (not elided) results asked for, by tool call id. */
  fullResults: Map<string, string>;
}

/**
 * Everything a copy or an export needs whole, in one round: the elided
 * inputs and results (by block id, one GetContentBlockBodies batch with
 * `full`) and the truncated results of an older server (GetToolResult, by
 * call). Throws when any of it cannot be read, so a caller never puts a
 * skeleton or a stub where the body belongs.
 */
export async function fetchFullToolBodies(
  chatId: string,
  { elidedBlockIds, truncatedCallIds }: { elidedBlockIds: Iterable<string>; truncatedCallIds: Iterable<string> },
): Promise<FullToolBodies> {
  const callIds = [...new Set(truncatedCallIds)];
  const [{ bodies, notFound }, contents] = await Promise.all([
    fetchContentBlockBodies(chatId, elidedBlockIds, { full: true }),
    Promise.all(callIds.map((callId) => fetchFullToolResult(chatId, callId))),
  ]);
  if (notFound.length > 0) {
    throw new Error(`Couldn't read ${notFound.length === 1 ? "a tool call's details" : `${notFound.length} tool calls' details`}`);
  }
  return { bodies, fullResults: new Map(callIds.map((callId, index) => [callId, contents[index]])) };
}
