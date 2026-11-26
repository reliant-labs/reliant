// Copyright (c) 2025 Reliant Labs

/**
 * StreamingService: the three server streams the signed-in app keeps open.
 *
 *   StreamUserUpdates  one sync frame (the user stream is at sequence 10),
 *                      then nothing: no chat changes while you look
 *   StreamChatUpdates  the chat's snapshot (its transcript, the failed send
 *                      included, and the errors its turns hit), "caught up",
 *                      then nothing
 *   StreamOrgUpdates   "caught up" at sequence 0 (nobody has shared anything
 *                      with the org), then nothing
 *
 * Each STAYS OPEN after its frames, as a live server's does, so the app never
 * reconnects. A stream ends only when its call is aborted (`ctx.signal`):
 * then it rejects with the abort reason. A caller that passes no signal
 * (forge's mock transport, under today's scenario dispatch, passes none)
 * gets a stream that never ends.
 */
import { create } from "@bufbuild/protobuf";
import type { HandlerContext, ServiceImpl } from "@connectrpc/connect";

import {
  ChatStreamEventSchema, OrgStreamEventSchema, StreamingService, UserStreamEventSchema, type ChatStreamEvent, type OrgStreamEvent, type StreamChatUpdatesRequest,
  type StreamOrgUpdatesRequest, type StreamUserUpdatesRequest, type UserStreamEvent,
} from "@/gen/reliant/v1/streaming_pb";

import { chatErrors, chatTranscript, LATEST_SEQUENCE } from "./chats";

/** A promise that never resolves, and rejects with the abort reason once `signal` aborts. */
export function untilAborted(signal: AbortSignal | undefined): Promise<never> {
  return new Promise<never>((_, reject) => {
    if (!signal) return;
    if (signal.aborted) return reject(signal.reason);
    signal.addEventListener("abort", () => reject(signal.reason), { once: true });
  });
}

/** What a stream handler is called with: Connect's HandlerContext, or (scenario dispatch) nothing. */
type StreamContext = Pick<HandlerContext, "signal"> | undefined;

/** The user stream's frames. */
export const userStreamEvents = (): UserStreamEvent[] => [create(UserStreamEventSchema, { event: { case: "sync", value: { lastSequence: 10n } } })];

/** The org feed's frames: caught up, with nothing to replay. */
export const orgStreamEvents = (): OrgStreamEvent[] => [create(OrgStreamEventSchema, { event: { case: "caughtUp", value: { latestSequence: 0n } } })];

/**
 * A chat stream's frames. `failedSend: false` leaves out the client-only
 * failed send: on the mobile chat screen whether it survives depends on the
 * order the snapshot and the message list land in, so a capture there leaves
 * it out (the harness's `chat-no-failed-send` overlay).
 */
export function chatStreamEvents(chatId: string, { failedSend = true }: { failedSend?: boolean } = {}): ChatStreamEvent[] {
  const all = chatTranscript(chatId);
  const messages = failedSend ? all : all.filter((m) => !m.id.startsWith("optimistic-"));
  return [
    create(ChatStreamEventSchema, {
      event: {
        case: "snapshot",
        value: {
          messages, otherUpdates: chatErrors(chatId), latestSequence: LATEST_SEQUENCE, total: messages.length, hasMore: false,
          oldestSeq: messages.length ? 1n : 0n, threadTokenCount: 92_400n, compactionThreshold: 120_000n, activeWorkflows: [],
        },
      },
    }),
    create(ChatStreamEventSchema, { event: { case: "caughtUp", value: { latestSequence: LATEST_SEQUENCE } } }),
  ];
}

/** Yield `frames`, then stay open until the call is aborted. */
async function* openStream<T>(frames: T[], ctx: StreamContext): AsyncGenerator<T> {
  yield* frames;
  await untilAborted(ctx?.signal);
}

/** The streams as a Connect ServiceImpl (`routes`): there each gets the call's HandlerContext. */
export const streamingImpl = {
  streamUserUpdates: (_req: StreamUserUpdatesRequest, ctx?: StreamContext) => openStream(userStreamEvents(), ctx),
  streamChatUpdates: (req: StreamChatUpdatesRequest, ctx?: StreamContext) => openStream(chatStreamEvents(req.chatId), ctx),
  streamOrgUpdates: (_req: StreamOrgUpdatesRequest, ctx?: StreamContext) => openStream(orgStreamEvents(), ctx),
} satisfies Partial<ServiceImpl<typeof StreamingService>>;

/** The streams as scenario handlers. */
export function streamingHandlers() {
  const key = (name: string) => `${StreamingService.typeName}/${name}`;
  return {
    [key("StreamUserUpdates")]: (req: StreamUserUpdatesRequest) => streamingImpl.streamUserUpdates(req),
    [key("StreamChatUpdates")]: (req: StreamChatUpdatesRequest) => streamingImpl.streamChatUpdates(req),
    [key("StreamOrgUpdates")]: (req: StreamOrgUpdatesRequest) => streamingImpl.streamOrgUpdates(req),
  };
}
