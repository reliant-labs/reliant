// Copyright (c) 2025 Reliant Labs

/**
 * ChatService reads that are plain request/response: the stream never writes
 * them (at most it says they changed), so they live on the generated keys
 * (`chatServiceKeys`) and are always some request's answer. The chat core —
 * list, detail, selection, messages — is stream-owned and stays on the hand
 * `chatKeys` / `messageKeys` (chat-queries.ts, message-queries.ts).
 *
 * The other pull reads have homes of their own: the archived list
 * (useArchivedChats, chat-queries.ts), a chat's execution tree
 * (useWorkflowExecutions.ts), a thread's mailbox (queued-agent-messages.ts)
 * and a run's step records (components/workflow/run/BuilderRunContext.tsx).
 */

import { useMemo } from "react";
import { useQuery, type QueryClient } from "@tanstack/react-query";
import type { Chat } from "../types/chat";
import type { SearchChatsResponse } from "../gen/reliant/v1/chat_pb";
import { protoValueToJs } from "../api/proto-utils";
import { keepPreviousInOrg, orgQueryEnabled, orgRequest, useOrgScope, withOrgFailure } from "../lib/orgScope";
import { queryClient as appQueryClient } from "../lib/query-client";
import { chatServiceQueries } from "./chat-service-hooks_gen";
import { useChatListWithWorkspaces } from "./chat-queries";
import { useDebounce } from "./useDebounce";

// ── Thread workflow inputs ──────────────────────────────────────────────────

export interface ThreadWorkflowInputs {
  workflowName: string;
  inputs: Record<string, unknown>;
  isRunning: boolean;
  /** A sub-agent that stopped on an error: a message resumes it, with these params. */
  resumable: boolean;
}

/**
 * The workflow inputs a thread is running with, read fresh each time (the
 * composer asks on every thread switch). Through the generated entry, so two
 * asks for one thread at once share a request.
 */
export async function fetchThreadWorkflowInputs(
  chatId: string,
  threadId: string,
  qc: QueryClient = appQueryClient,
): Promise<ThreadWorkflowInputs> {
  const response = await qc.fetchQuery(
    chatServiceQueries.getThreadWorkflowInputs({ chatId, threadId }, { staleTime: 0 }),
  );
  const inputs: Record<string, unknown> = {};
  for (const [key, value] of Object.entries(response.inputs)) {
    inputs[key] = protoValueToJs(value);
  }
  return {
    workflowName: response.workflowName,
    inputs,
    isRunning: response.isRunning,
    resumable: response.resumable,
  };
}

// ── Chat search ─────────────────────────────────────────────────────────────

/** How long typing must pause before a search is sent. */
export const CHAT_SEARCH_DEBOUNCE_MS = 300;
const RECENT_LIMIT = 10;
const RESULT_LIMIT = 15;

export interface ChatSearchHit {
  chatId: string;
  title: string;
  updatedAt: string;
}

const NO_CHATS: Chat[] = [];

function toHit(chat: { id: string; title: string; updatedAt: string }): ChatSearchHit {
  return { chatId: chat.id, title: chat.title, updatedAt: chat.updatedAt };
}

function hitsOf(response: SearchChatsResponse): ChatSearchHit[] {
  return response.chats.slice(0, RESULT_LIMIT).map(toHit);
}

function recentOf(chats: Chat[]): ChatSearchHit[] {
  return [...chats]
    .sort((a, b) => new Date(b.updatedAt).getTime() - new Date(a.updatedAt).getTime())
    .slice(0, RECENT_LIMIT)
    .map(toHit);
}

function titleMatches(chats: Chat[], term: string): ChatSearchHit[] {
  const needle = term.toLowerCase();
  return chats
    .filter((chat) => chat.title.toLowerCase().includes(needle))
    .slice(0, RESULT_LIMIT)
    .map(toHit);
}

export interface ChatSearch {
  /** The most recent chats for an empty query, otherwise the matches. */
  results: ChatSearchHit[];
  /** A server search is in flight. */
  isLoading: boolean;
}

/**
 * Find a chat by title in a scope — one project, or every project when
 * `scope` is null (the sidebar's filter): the server's full-text search
 * (SearchChats) on the debounced query, or — when the search fails — a title
 * match over the scope's chat list. An empty query lists the most recent
 * chats. While a new term is in flight the previous term's matches stay on
 * screen.
 */
export function useChatSearch(scope: string | null, query: string, enabled = true): ChatSearch {
  const { data: list } = useChatListWithWorkspaces(scope, { enabled });
  const chats = list?.chats ?? NO_CHATS;
  const term = useDebounce(query.trim(), CHAT_SEARCH_DEBOUNCE_MS);
  const searchesServer = enabled && term !== "";
  const org = useOrgScope();
  const search = useQuery(
    withOrgFailure(
      chatServiceQueries.searchChats(orgRequest({ projectId: scope ?? "", query: term }, org.orgId), {
        // Not before the tab's org is known: a search naming no org is every org's.
        enabled: searchesServer && orgQueryEnabled(org),
        select: hitsOf,
        // A failed search falls back to the title match at once.
        retry: false,
        // The previous term's hits stay up while the next term loads, but
        // never another org's: after a switch, nothing until this org answers.
        placeholderData: keepPreviousInOrg(org.orgId),
      }),
      org,
    ),
  );
  const serverHits = search.data;
  const failed = search.isError;
  const results = useMemo(() => {
    if (!term) return recentOf(chats);
    if (searchesServer && !failed) return serverHits ?? [];
    return titleMatches(chats, term);
  }, [term, chats, searchesServer, failed, serverHits]);
  return { results, isLoading: searchesServer && search.isFetching };
}
