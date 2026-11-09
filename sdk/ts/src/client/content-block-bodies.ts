// The bodies a toolBodies: "stub" transcript read left out, read back by block
// id (ChatService.GetContentBlockBodies). A stub read sends each finished tool
// call's input as a skeleton and its result as a short stub, flagged
// body_elided on the block; a host fetches the body when it shows the call.
//
// Bodies are not folded into ChatState: a finished call's body never changes,
// so a host keeps them in a cache of its own (per block id), and the state
// never holds megabytes of bodies nobody opened.

import { ToolBodyMode } from "#gen/reliant/v1/chat_pb.js";
import type { ContentBlockBody, ToolBodies } from "./types.js";

/** The most block ids one GetContentBlockBodies request may name; more is INVALID_ARGUMENT. */
export const MAX_CONTENT_BLOCK_BODY_IDS = 100;

/**
 * The one rpc fetchContentBlockBodies calls, typed by what it reads. A Connect
 * client of reliant.v1.ChatService satisfies it, whichever generated copy of
 * the service it was built from.
 */
export interface ContentBlockBodiesClient {
  getContentBlockBodies(
    request: { chatId: string; blockIds: string[]; full?: boolean },
    options?: { signal?: AbortSignal },
  ): Promise<{ bodies: ContentBlockBody[]; notFound: string[]; deferred: string[] }>;
}

export interface FetchContentBlockBodiesOptions {
  /**
   * Results whole rather than as the 8 KiB display preview an expanded row
   * shows (that preview is marked `truncated`, as in any transcript read): for
   * copy and export.
   */
  full?: boolean;
  signal?: AbortSignal;
}

export interface ContentBlockBodies {
  /** The bodies, by block id. */
  bodies: Record<string, ContentBlockBody>;
  /**
   * Ids that are not blocks this chat displays: unknown, another chat's, or
   * hidden. Asking again will not find them.
   */
  notFound: string[];
}

/**
 * Read the bodies of a chat's blocks by id: the tool inputs, tool results and
 * thinking a toolBodies: "stub" read sent elided (ContentBlock.body_elided).
 *
 * Any number of ids: they are deduplicated and sent at most
 * MAX_CONTENT_BLOCK_BODY_IDS per request, one request at a time, and the ids a
 * response defers (to stay under its byte budget) are asked for again. Every
 * id comes back in exactly one of `bodies` and `notFound`. A server that has
 * no such rpc yet answers Unimplemented, which is thrown: the host shows that
 * row's error and retries later.
 */
export async function fetchContentBlockBodies(
  client: ContentBlockBodiesClient,
  chatId: string,
  blockIds: Iterable<string>,
  options: FetchContentBlockBodiesOptions = {},
): Promise<ContentBlockBodies> {
  const result: ContentBlockBodies = { bodies: {}, notFound: [] };
  const queue = [...new Set([...blockIds].filter((id) => id !== ""))];
  while (queue.length > 0) {
    const asked = queue.splice(0, MAX_CONTENT_BLOCK_BODY_IDS);
    const response = await client.getContentBlockBodies(
      { chatId, blockIds: asked, full: options.full ? true : undefined },
      { signal: options.signal },
    );
    // Only ids this request named count as answered: an id the server names
    // that was not asked (or was answered before) is not progress.
    const pending = new Set(asked);
    const answered = new Set<string>();
    for (const body of response.bodies) {
      if (!pending.has(body.blockId) || answered.has(body.blockId)) continue;
      result.bodies[body.blockId] = body;
      answered.add(body.blockId);
    }
    for (const id of response.notFound) {
      if (!pending.has(id) || answered.has(id)) continue;
      result.notFound.push(id);
      answered.add(id);
    }
    // The server always answers at least one asked id, so every round removes
    // at least one id from the queue and the loop ends: a server that keeps
    // deferring an id still settles the others, and once that id is the only
    // one left, a response that answers nothing is thrown, not retried.
    if (answered.size === 0) {
      throw new Error(`GetContentBlockBodies answered none of the ${asked.length} ids it was asked for`);
    }
    const deferred = new Set(response.deferred);
    const retry: string[] = [];
    for (const id of asked) {
      if (answered.has(id)) continue;
      // An id in none of the three lists is not one the server can find.
      if (deferred.has(id)) retry.push(id);
      else result.notFound.push(id);
    }
    queue.unshift(...retry);
  }
  return result;
}

/** The ToolBodyMode a request carries for a toolBodies option: unset (FULL to every server) unless "stub". */
export function toolBodyModeOf(toolBodies: ToolBodies | undefined): ToolBodyMode | undefined {
  return toolBodies === "stub" ? ToolBodyMode.STUB : undefined;
}
