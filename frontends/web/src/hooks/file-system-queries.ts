// Copyright (c) 2025 Reliant Labs

/**
 * FileSystemService reads that are plain request/response, on forge's
 * generated hooks: content search (SearchFiles) and the directory picker's
 * listings (ListDirectory). Each entry is one request's answer, so refetching
 * it is always correct, and nothing on the stream touches either.
 *
 * The rest of the service stays hand-written in api/fileSystem.ts: the file
 * tree (assembled by FileTree, folder by folder), file content (an editor
 * buffer a refetch must never overwrite) and the writes.
 *
 * What refetches these:
 *   - a ReplaceInFiles re-runs every mounted search (api/fileSystem.ts), its
 *     matches being gone;
 *   - the generated CreateDirectory refetches every mounted listing;
 *   - nothing else. The hand-written writes (save, create, delete, copy) do
 *     not touch this cache, and a search never refetches on window focus: it
 *     can walk a whole repository on a remote machine, and its results carry
 *     the user's expand/select state.
 */

import { keepPreviousData, type QueryClient } from "@tanstack/react-query";
import type {
  ListDirectoryResponse,
  SearchFilesResponse,
} from "../gen/reliant/v1/filesystem_pb";
import { queryClient as appQueryClient } from "../lib/query-client";
import { useChatStore } from "../store/chatStore";
import { useProjectStore } from "../store/projectStore";
import { fileSystemServiceQueries, useSearchFiles } from "./file-system-service-hooks_gen";
import { useDebounce } from "./useDebounce";

// ── Content search ──────────────────────────────────────────────────────────

export interface SearchMatch {
  lineNumber: number;
  lineContent: string;
  matchStart: number;
  matchEnd: number;
  contextBefore: string[];
  contextAfter: string[];
}

export interface SearchResult {
  path: string;
  matches: SearchMatch[];
}

/** How long typing must pause before a search is sent. */
export const FILE_SEARCH_DEBOUNCE_MS = 300;

export interface FileSearchOptions {
  /** The workspace to search; undefined is the project's main checkout. */
  worktreeId?: string;
  caseSensitive: boolean;
  /** A glob such as `*.ts`; empty searches every file. */
  filePattern?: string;
  maxResults: number;
  contextLines: number;
  /** False while the search UI is closed. Defaults to true. */
  enabled?: boolean;
}

export interface FileSearch {
  results: SearchResult[];
  totalMatches: number;
  truncated: boolean;
  /** A search for the current term and options is in flight. */
  isSearching: boolean;
  error: Error | null;
  /** Search again now (a machine came online). */
  refetch: () => void;
}

const NO_RESULTS: SearchResult[] = [];
const IDLE: FileSearch = {
  results: NO_RESULTS,
  totalMatches: 0,
  truncated: false,
  isSearching: false,
  error: null,
  refetch: () => {},
};

interface SearchAnswer {
  results: SearchResult[];
  totalMatches: number;
  truncated: boolean;
}

/** Hoisted, so React Query re-runs the mapping only when the response changes. */
function toSearchAnswer(response: SearchFilesResponse): SearchAnswer {
  return {
    results: response.results.map((r) => ({
      path: r.path,
      matches: r.matches.map((m) => ({
        lineNumber: m.lineNumber,
        lineContent: m.lineContent,
        matchStart: m.matchStart,
        matchEnd: m.matchEnd,
        contextBefore: [...m.contextBefore],
        contextAfter: [...m.contextAfter],
      })),
    })),
    totalMatches: response.totalMatches,
    truncated: response.truncated,
  };
}

/**
 * Search file contents in the open project, on the open chat's machine (the
 * server routes by the chat: two chats can be two disks).
 *
 * The query is debounced; the term and every option are the cache key, so
 * changing an option searches again at once and returning to an earlier term
 * shows its results while they are re-read. While a new search is in flight
 * the previous results stay on screen. An empty query searches nothing.
 */
export function useFileSearch(query: string, options: FileSearchOptions): FileSearch {
  const projectId = useProjectStore((s) => s.currentProject?.id);
  const chatId = useChatStore((s) => s.activeChatId) ?? undefined;
  const term = useDebounce(query, FILE_SEARCH_DEBOUNCE_MS);
  const searching = (options.enabled ?? true) && !!projectId && term.trim() !== "";

  const search = useSearchFiles(
    {
      projectId: projectId ?? "",
      query: term,
      worktreeId: options.worktreeId,
      chatId,
      filePattern: options.filePattern || undefined,
      caseSensitive: options.caseSensitive,
      maxResults: options.maxResults,
      contextLines: options.contextLines,
    },
    {
      enabled: searching,
      select: toSearchAnswer,
      // Re-read on every new mount of a term, never on focus (module doc).
      staleTime: 0,
      refetchOnWindowFocus: false,
      // A search box shows its error at once; a retry cannot bring a machine up.
      retry: false,
      placeholderData: keepPreviousData,
    },
  );

  if (!searching) return IDLE;
  return {
    results: search.data?.results ?? NO_RESULTS,
    totalMatches: search.data?.totalMatches ?? 0,
    truncated: search.data?.truncated ?? false,
    isSearching: search.isFetching,
    error: search.error,
    refetch: () => void search.refetch(),
  };
}

// ── Directory listings ──────────────────────────────────────────────────────

/**
 * One directory on the user's machine, outside any project: `""` is the home
 * directory. Read fresh (`staleTime: 0`): the default machine can change.
 */
export function fetchDirectoryListing(path: string, qc: QueryClient = appQueryClient): Promise<ListDirectoryResponse> {
  return qc.fetchQuery(fileSystemServiceQueries.listDirectory({ path }, { staleTime: 0 }));
}
