// Copyright (c) 2025 Reliant Labs

/**
 * The bodies of the tool calls a chat was opened without.
 *
 * A transcript read that asks for stubs (lib/toolBodies.ts) sends each
 * finished call's input as a skeleton and its result as a short stub, flagged
 * `bodyElided` on the block. The body is read back by block id
 * (ChatService.GetContentBlockBodies) when a row shows it: an expanded
 * ToolExecution, an opened fold, a copy or an export.
 *
 * Bodies live in React Query, one entry per block
 * (`["contentBlockBody", chatId, blockId, full]`), never in the message
 * objects: a finished call's body never changes (staleTime Infinity), the
 * store never holds megabytes nobody opened, and processMessage's
 * reference-keyed memo stays valid.
 *
 * Behind the cache a batcher collects the ids asked for within one frame and
 * sends them as one request (the SDK's fetchContentBlockBodies splits it into
 * chunks of 100 and asks again for what the server deferred). An id the server
 * says it cannot find is remembered, so a remount does not ask again.
 *
 * dev-docs/design/chat-lazy-tool-bodies.md, §3.
 */

import { useQuery } from "@tanstack/react-query";
import {
  fetchContentBlockBodies as fetchBodiesFromServer,
  type ContentBlockBodiesClient,
  type ContentBlockBody,
} from "@reliantlabs/sdk/client";
import { ChatService } from "../gen/reliant/v1/chat_pb";
import { connectClient } from "../lib/connect";
import { queryClient as appQueryClient } from "../lib/query-client";

export type { ContentBlockBody };

/** The server does not show this block in this chat (unknown, another chat's, or hidden). */
export class ContentBlockBodyNotFoundError extends Error {
  readonly blockId: string;
  constructor(blockId: string) {
    super(`content block ${blockId} was not found`);
    this.name = "ContentBlockBodyNotFoundError";
    this.blockId = blockId;
  }
}

/** One fetched body, and whether the server deferred it once (telemetry). */
export interface LoadedContentBlockBody {
  body: ContentBlockBody;
  deferred: boolean;
}

type Waiter = {
  resolve: (loaded: LoadedContentBlockBody) => void;
  reject: (error: unknown) => void;
};

/** Runs `flush` once, soon: the next frame, or a timer when frames are not coming. */
export type BatchScheduler = (flush: () => void) => void;

/**
 * The next animation frame, so rows mounting in the same frame share a
 * request. A hidden tab paints no frames, so a short timer stands in (and
 * covers a frame that never comes).
 */
const nextFrame: BatchScheduler = (flush) => {
  let ran = false;
  const once = () => {
    if (ran) return;
    ran = true;
    flush();
  };
  if (
    typeof requestAnimationFrame === "function" &&
    typeof document !== "undefined" &&
    document.visibilityState === "visible"
  ) {
    requestAnimationFrame(once);
  }
  setTimeout(once, 50);
};

export interface ContentBlockBodyBatcher {
  /** The body of one block; coalesced with every other id asked for before the flush. */
  load(chatId: string, blockId: string, full: boolean): Promise<LoadedContentBlockBody>;
  /** Ask the server again for an id it said it could not find (an explicit Retry). */
  forgetNotFound(chatId: string, blockId: string): void;
}

/**
 * The batcher behind the cache. Exported with its client and scheduler as
 * parameters so a test can drive the flush.
 */
export function createContentBlockBodyBatcher(
  client: () => ContentBlockBodiesClient,
  schedule: BatchScheduler = nextFrame,
): ContentBlockBodyBatcher {
  // chatId + full -> blockId -> waiters. One request per (chat, full) per flush.
  const pending = new Map<string, { chatId: string; full: boolean; ids: Map<string, Waiter[]> }>();
  const notFound = new Set<string>();
  let scheduled = false;

  const notFoundKey = (chatId: string, blockId: string) => `${chatId}\u0000${blockId}`;

  async function send(chatId: string, full: boolean, ids: Map<string, Waiter[]>): Promise<void> {
    const deferred = new Set<string>();
    // Counts what the server deferred; the SDK helper asks for those again.
    const tracking: ContentBlockBodiesClient = {
      getContentBlockBodies: async (request, options) => {
        const response = await client().getContentBlockBodies(request, options);
        for (const id of response.deferred) deferred.add(id);
        return response;
      },
    };
    try {
      const result = await fetchBodiesFromServer(tracking, chatId, ids.keys(), { full });
      for (const [blockId, waiters] of ids) {
        const body = result.bodies[blockId];
        if (body) {
          const loaded = { body, deferred: deferred.has(blockId) };
          for (const waiter of waiters) waiter.resolve(loaded);
        } else {
          notFound.add(notFoundKey(chatId, blockId));
          const error = new ContentBlockBodyNotFoundError(blockId);
          for (const waiter of waiters) waiter.reject(error);
        }
      }
    } catch (error) {
      for (const waiters of ids.values()) for (const waiter of waiters) waiter.reject(error);
    }
  }

  function flush(): void {
    scheduled = false;
    const groups = [...pending.values()];
    pending.clear();
    for (const group of groups) void send(group.chatId, group.full, group.ids);
  }

  return {
    load(chatId, blockId, full) {
      if (notFound.has(notFoundKey(chatId, blockId))) {
        return Promise.reject(new ContentBlockBodyNotFoundError(blockId));
      }
      return new Promise<LoadedContentBlockBody>((resolve, reject) => {
        const key = `${chatId}\u0000${full ? "full" : "display"}`;
        let group = pending.get(key);
        if (!group) {
          group = { chatId, full, ids: new Map() };
          pending.set(key, group);
        }
        const waiters = group.ids.get(blockId);
        if (waiters) waiters.push({ resolve, reject });
        else group.ids.set(blockId, [{ resolve, reject }]);
        if (!scheduled) {
          scheduled = true;
          schedule(flush);
        }
      });
    },
    forgetNotFound(chatId, blockId) {
      notFound.delete(notFoundKey(chatId, blockId));
    },
  };
}

const batcher = createContentBlockBodyBatcher(() => connectClient(ChatService));

/** The cache key of one block's body; `full` is a result whole rather than its 8 KiB display preview. */
export function contentBlockBodyQueryKey(chatId: string, blockId: string, full: boolean) {
  return ["contentBlockBody", chatId, blockId, full] as const;
}

function contentBlockBodyQuery(chatId: string, blockId: string, full: boolean) {
  return {
    queryKey: contentBlockBodyQueryKey(chatId, blockId, full),
    queryFn: () => batcher.load(chatId, blockId, full),
    // A finished call's body never changes: never refetch what was read.
    staleTime: Infinity,
    // Bodies can be large; let one nobody is showing go after a while.
    gcTime: 10 * 60 * 1000,
    // A not-found is an answer, not a failure: asking again finds nothing.
    retry: (failures: number, error: unknown) =>
      !(error instanceof ContentBlockBodyNotFoundError) && failures < 1,
  };
}

/** The cached body of a block, if this session already read it. */
export function cachedContentBlockBody(
  chatId: string,
  blockId: string,
  full: boolean,
): ContentBlockBody | undefined {
  const exact = appQueryClient.getQueryData<LoadedContentBlockBody>(contentBlockBodyQueryKey(chatId, blockId, full))?.body;
  if (exact || !full) return exact;
  // A display body that is not truncated is already whole (inputs always
  // are), so a full read can take it rather than ask again.
  const display = appQueryClient.getQueryData<LoadedContentBlockBody>(contentBlockBodyQueryKey(chatId, blockId, false))?.body;
  return display && !display.truncated ? display : undefined;
}

/**
 * The body a row shows, fetched while `enabled`. One fetched earlier (a fold
 * prefetch, a copy) is returned from the cache even when not enabled.
 *
 * Bound to the app's query client, the one every other read of a body fills,
 * so the row, the fold and a copy share one entry wherever the row renders.
 */
export function useContentBlockBody(
  chatId: string | undefined,
  blockId: string | undefined,
  enabled: boolean,
) {
  return useQuery(
    {
      ...contentBlockBodyQuery(chatId ?? "", blockId ?? "", false),
      enabled: enabled && !!chatId && !!blockId,
    },
    appQueryClient,
  );
}

/** Retry a body the row could not load, including one the server did not find. */
export function retryContentBlockBody(chatId: string, blockId: string): Promise<unknown> {
  batcher.forgetNotFound(chatId, blockId);
  return appQueryClient.refetchQueries({ queryKey: contentBlockBodyQueryKey(chatId, blockId, false), exact: true });
}

/**
 * Warm the cache with the display bodies of `blockIds`, in one batch: what an
 * opening fold does for its rows, so the rows that render expanded do not
 * each start their own request.
 */
export function prefetchContentBlockBodies(chatId: string, blockIds: Iterable<string>): void {
  for (const blockId of new Set(blockIds)) {
    if (!blockId) continue;
    void appQueryClient.prefetchQuery(contentBlockBodyQuery(chatId, blockId, false));
  }
}

export interface FetchedContentBlockBodies {
  bodies: Record<string, ContentBlockBody>;
  /** Ids the server does not show in this chat. */
  notFound: string[];
}

/**
 * The bodies of `blockIds`, from the cache or the server in one batch.
 * `full` reads results whole (copy, export) rather than as the display
 * preview. Throws when the server could not be asked (the caller shows the
 * error); an id it does not show comes back in `notFound`.
 */
export async function fetchContentBlockBodies(
  chatId: string,
  blockIds: Iterable<string>,
  { full = false }: { full?: boolean } = {},
): Promise<FetchedContentBlockBodies> {
  const ids = [...new Set(blockIds)].filter(Boolean);
  const settled = await Promise.allSettled(
    ids.map(async (blockId) => {
      const cached = cachedContentBlockBody(chatId, blockId, full);
      if (cached) return cached;
      const loaded = await appQueryClient.fetchQuery({ ...contentBlockBodyQuery(chatId, blockId, full), retry: false });
      return loaded.body;
    }),
  );
  const result: FetchedContentBlockBodies = { bodies: {}, notFound: [] };
  settled.forEach((outcome, index) => {
    const blockId = ids[index];
    if (outcome.status === "fulfilled") result.bodies[blockId] = outcome.value;
    else if (outcome.reason instanceof ContentBlockBodyNotFoundError) result.notFound.push(blockId);
    else throw outcome.reason;
  });
  return result;
}

/** What elidedBlockIdsOf reads of an execution (a structural subset of messageProcessor's). */
export interface ExecutionWithBodies {
  call: { inputElided?: boolean; content_block_id?: string };
  result?: { elided?: boolean; block_id?: string };
}

/** The block ids of every body `executions` were sent without: elided inputs and results. */
export function elidedBlockIdsOf(executions: ReadonlyArray<ExecutionWithBodies>): string[] {
  const ids: string[] = [];
  for (const { call, result } of executions) {
    if (call.inputElided && call.content_block_id) ids.push(call.content_block_id);
    if (result?.elided && result.block_id) ids.push(result.block_id);
  }
  return ids;
}
