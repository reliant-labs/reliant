// A scripted Connect server for tests: a Transport whose streaming calls are
// driven by hand, one connection at a time, and whose unary calls are answered
// by handlers. Not part of the published package.

import { create, type DescMessage } from "@bufbuild/protobuf";
import { Code, ConnectError, type Transport } from "@connectrpc/connect";
import { ChatStreamEventSchema, ChatUpdateType } from "#gen/reliant/v1/streaming_pb.js";
import { StreamingService } from "#gen/reliant/v1/streaming_pb.js";
import { createClient } from "@connectrpc/connect";
import type { StreamingClient } from "../client/user-stream.js";

export interface Connection {
  method: string;
  request: Record<string, unknown>;
  signal: AbortSignal | undefined;
  push(event: unknown): void;
  fail(error: Error): void;
  end(): void;
}

export type UnaryHandler = (request: Record<string, unknown>) => unknown | Promise<unknown>;

export function scriptedServer(unary: Record<string, UnaryHandler> = {}) {
  const connections: Connection[] = [];
  const unaryRequests: Array<{ method: string; request: Record<string, unknown> }> = [];
  const transport: Transport = {
    async unary(method, _signal, _timeout, _header, input) {
      const request = create(method.input as DescMessage, input as never) as unknown as Record<string, unknown>;
      unaryRequests.push({ method: method.name, request });
      const handler = unary[method.name];
      if (!handler) throw new ConnectError(`scripted server: no handler for ${method.name}`, Code.Unimplemented);
      const result = await handler(request);
      return {
        stream: false,
        service: method.parent,
        method,
        header: new Headers(),
        trailer: new Headers(),
        message: create(method.output as DescMessage, (result ?? {}) as never),
      } as never;
    },
    async stream(method, signal, _timeout, _header, input) {
      let request: Record<string, unknown> = {};
      for await (const first of input) {
        request = create(method.input as DescMessage, first as never) as unknown as Record<string, unknown>;
        break;
      }
      const queue: Array<{ event: unknown } | { error: Error } | { done: true }> = [];
      let wake: (() => void) | null = null;
      const enqueue = (item: (typeof queue)[number]): void => {
        queue.push(item);
        wake?.();
      };
      signal?.addEventListener("abort", () => enqueue({ error: new ConnectError("aborted", Code.Canceled) }));
      connections.push({
        method: method.name,
        request,
        signal,
        push: (event) => enqueue({ event }),
        fail: (error) => enqueue({ error }),
        end: () => enqueue({ done: true }),
      });
      return {
        stream: true,
        service: method.parent,
        method,
        header: new Headers(),
        trailer: new Headers(),
        message: {
          async *[Symbol.asyncIterator]() {
            for (;;) {
              while (queue.length === 0) await new Promise<void>((r) => (wake = r));
              const item = queue.shift()!;
              if ("event" in item) yield item.event;
              else if ("error" in item) throw item.error;
              else return;
            }
          },
        },
      } as never;
    },
  };
  return {
    transport,
    connections,
    unaryRequests,
    streamingClient: (): StreamingClient => createClient(StreamingService, transport),
    chatStreams: () => connections.filter((c) => c.method === "StreamChatUpdates"),
  };
}

// ── Wire frames, as the server sends them ───────────────────────────────────

export const CHAT = "c0ffee00-1111-2222-3333-444444444444";

export const frame = (event: Record<string, unknown>) =>
  create(ChatStreamEventSchema, { event } as never);

export const heartbeatFrame = () => frame({ case: "heartbeat", value: { timestamp: 0n } });
export const caughtUpFrame = (latest: number) =>
  frame({ case: "caughtUp", value: { latestSequence: BigInt(latest) } });
export const updatesFrame = (updates: unknown[], latest: number) =>
  frame({ case: "updates", value: { updates, latestSequence: BigInt(latest) } });

export const textMessage = (id: string, role: number, seq: number, text: string, thread = "") => ({
  id,
  chatId: CHAT,
  role,
  seq: BigInt(seq),
  thread,
  streamingState: 2,
  createdAt: "2026-01-01T00:00:00.000Z",
  updatedAt: "2026-01-01T00:00:00.000Z",
  contentBlocks: [{ id: `${id}-b0`, type: 1, index: 0, content: text }],
});

export const snapshotFrame = (latest: number, messages: unknown[], otherUpdates: unknown[] = []) =>
  frame({
    case: "snapshot",
    value: {
      messages,
      otherUpdates,
      latestSequence: BigInt(latest),
      total: messages.length,
      hasMore: false,
      oldestSeq: 1n,
      threadTokenCount: 120n,
      compactionThreshold: 20000n,
    },
  });

export const updateData = (type: ChatUpdateType, seq: number, entityId: string, payload: unknown) => ({
  updateType: type,
  sequenceNumber: BigInt(seq),
  entityId,
  chatId: CHAT,
  dataJson: JSON.stringify(payload),
});

export const approvalData = (seq: number, status: string) =>
  updateData(ChatUpdateType.APPROVAL, seq, "appr-1", {
    id: "appr-1",
    chat_id: CHAT,
    approval_type: "tool_call",
    entity_id: "tc-1",
    title: "Run the migration",
    status,
    sequence_number: seq,
    created_at: "2026-01-01T00:00:01.000Z",
  });

export const messageData = (seq: number, id: string, text: string, role = 2, thread = "") =>
  updateData(ChatUpdateType.MESSAGE, seq, id, {
    message: {
      id,
      chat_id: CHAT,
      role,
      seq,
      thread,
      streaming_state: 2,
      content_blocks: [{ id: `${id}-b0`, type: 1, index: 0, content: text }],
    },
  });

export const deltaData = (deltaType: string, extra: Record<string, unknown> = {}, messageId = "m-live") =>
  updateData(ChatUpdateType.STREAMING_DELTA, 0, messageId, {
    delta_type: deltaType,
    block_index: 0,
    message_id: messageId,
    thread: "",
    ...extra,
  });

export const toolCallData = (seq: number, toolCallId: string, status: string) =>
  updateData(ChatUpdateType.TOOL_CALL, seq, toolCallId, {
    tool_call_id: toolCallId,
    tool_name: "shell",
    status,
    sequence_number: seq,
  });

export const finalizedData = (seq: number, messageId: string, reason: string) =>
  updateData(ChatUpdateType.STREAM_FINALIZED, seq, messageId, { message_id: messageId, reason });
