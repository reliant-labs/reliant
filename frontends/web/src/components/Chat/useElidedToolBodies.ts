/**
 * A tool row's bodies, when the transcript sent them elided.
 *
 * A stub read (lib/toolBodies.ts) sends a finished call's input as a skeleton
 * and its result as a stub. The header and the collapsed row read those
 * as they are: the skeleton keeps every key a label reads. What the open row
 * shows is the body, fetched by block id while the row is `shown`
 * (hooks/content-block-bodies.ts). Until both bodies are here the row has no
 * input or result to render: `ready` is false and the row shows a placeholder
 * — a renderer is never handed a skeleton as if it were the input, which
 * would show a cut diff as a real one.
 *
 * A row with nothing elided (a FULL read, a live turn, an always-full tool)
 * gets its input and result back untouched and fetches nothing.
 *
 * `noteUserExpand` records the `tool_body.expand` interaction (click to body
 * painted) when the user opens a row that has bodies to fetch.
 */

import { useCallback, useEffect, useMemo, useState } from "react";
import { interaction, useInteractionEnd, type Interaction } from "@reliantlabs/forge-web-runtime";
import {
  cachedContentBlockBody,
  retryContentBlockBody,
  useContentBlockBody,
} from "../../hooks/content-block-bodies";
import type { ToolResultData } from "./tool-renderers/types";

export const TOOL_BODY_EXPAND_INTERACTION = "tool_body.expand";

type ToolInputValue = Record<string, unknown> | string;

export interface ElidedToolBodiesArgs {
  chatId?: string;
  toolName: string;
  call: {
    input?: ToolInputValue;
    content_block_id?: string;
    inputElided?: boolean;
    inputSize?: number;
  };
  result?: ToolResultData;
  /** The row shows its body (expanded, or an approval showing rich content). */
  shown: boolean;
  /** The row is expanded: an expand interaction still open when it closes ends as cancelled. */
  expanded: boolean;
}

export interface ElidedToolBodies {
  /** Whether anything of this call was elided at all. */
  elided: boolean;
  /** The input to render: the body, or `call.input` when it was not elided. Undefined until it arrives. */
  input: ToolInputValue | undefined;
  /** The result to render, likewise. */
  result: ToolResultData | undefined;
  /** Every elided body is here: the row can render its content. */
  ready: boolean;
  /** A body could not be fetched (or cannot be: no block id). */
  failed: boolean;
  /** Byte size of what is being fetched, for the placeholder's height. */
  bytes: number;
  retry: () => void;
  /** The user is opening the row: start the expand interaction if there is anything to fetch. */
  noteUserExpand: () => void;
}

function parseInput(raw: string | undefined): ToolInputValue {
  if (!raw) return {};
  try {
    return JSON.parse(raw) as Record<string, unknown>;
  } catch {
    return raw;
  }
}

export function useElidedToolBodies({
  chatId,
  toolName,
  call,
  result,
  shown,
  expanded,
}: ElidedToolBodiesArgs): ElidedToolBodies {
  const inputElided = !!call.inputElided;
  const resultElided = !!result?.elided;
  const inputBlockId = inputElided ? call.content_block_id : undefined;
  const resultBlockId = resultElided ? result?.block_id : undefined;
  // Elided with no way to ask for the body: there is nothing to fetch, and
  // the skeleton is still not the input.
  const unreachable = (inputElided && (!inputBlockId || !chatId)) || (resultElided && (!resultBlockId || !chatId));

  const inputQuery = useContentBlockBody(chatId, inputBlockId, shown);
  const resultQuery = useContentBlockBody(chatId, resultBlockId, shown);

  const rawInputBody = inputQuery.data?.body.input;
  const input = useMemo<ToolInputValue | undefined>(() => {
    if (!inputElided) return call.input;
    return inputQuery.data ? parseInput(rawInputBody) : undefined;
  }, [inputElided, call.input, inputQuery.data, rawInputBody]);

  const resultBody = resultQuery.data?.body;
  const effectiveResult = useMemo<ToolResultData | undefined>(() => {
    if (!result || !resultElided) return result;
    if (!resultBody) return undefined;
    return {
      ...result,
      content: resultBody.content ?? "",
      // The display body is itself a preview when the output is over 8 KiB:
      // the row then offers the full output as for any truncated result.
      truncated: resultBody.truncated || undefined,
      original_size: resultBody.originalSize !== undefined ? Number(resultBody.originalSize) : undefined,
      elided: false,
    };
  }, [result, resultElided, resultBody]);

  const elided = inputElided || resultElided;
  const inputReady = !inputElided || !!inputQuery.data;
  const resultReady = !resultElided || !!resultQuery.data;
  const ready = !unreachable && inputReady && resultReady;
  // A Retry in flight shows the placeholder again, not the error it replaces.
  const failed =
    unreachable ||
    (inputElided && !inputReady && inputQuery.isError && !inputQuery.isFetching) ||
    (resultElided && !resultReady && resultQuery.isError && !resultQuery.isFetching);
  const bytes = (inputElided ? call.inputSize ?? 0 : 0) + (resultElided ? result?.original_size ?? 0 : 0);
  const deferred = !!inputQuery.data?.deferred || !!resultQuery.data?.deferred;

  const retry = useCallback(() => {
    if (!chatId) return;
    if (inputBlockId && inputQuery.isError) void retryContentBlockBody(chatId, inputBlockId);
    if (resultBlockId && resultQuery.isError) void retryContentBlockBody(chatId, resultBlockId);
  }, [chatId, inputBlockId, resultBlockId, inputQuery.isError, resultQuery.isError]);

  // The expand interaction: from the click to the first paint with the body
  // (or the error) on screen.
  const [expandOp, setExpandOp] = useState<Interaction | null>(null);
  const noteUserExpand = useCallback(() => {
    if (!elided || !chatId) return;
    const ids = [inputBlockId, resultBlockId].filter((id): id is string => !!id);
    const hit = ids.length > 0 && ids.every((id) => cachedContentBlockBody(chatId, id, false) !== undefined);
    setExpandOp(
      interaction(
        TOOL_BODY_EXPAND_INTERACTION,
        {
          "chat.id": chatId,
          "tool.name": toolName,
          "tool_body.blocks": ids.length,
          "tool_body.bytes": bytes,
          "tool_body.cache": hit ? "hit" : "miss",
        },
        // Its own scope: an expand must not supersede the chat open.
        { scope: "tool_body" },
      ),
    );
  }, [elided, chatId, inputBlockId, resultBlockId, toolName, bytes]);
  useInteractionEnd(expandOp, ready || failed, {
    outcome: failed ? "error" : "ok",
    "tool_body.deferred": deferred,
  });
  useEffect(() => {
    if (!expanded && expandOp && !expandOp.ended) expandOp.end({ outcome: "cancelled" });
  }, [expanded, expandOp]);

  return { elided, input, result: effectiveResult, ready, failed, bytes, retry, noteUserExpand };
}
