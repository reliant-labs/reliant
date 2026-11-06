// createReliantClient: the entry point. A transport, and the streams and
// calls that ride on it.

import type { DescService } from "@bufbuild/protobuf";
import { createClient, type Client, type Transport } from "@connectrpc/connect";
import { BackgroundService } from "#gen/reliant/v1/background_pb.js";
import { ChatService } from "#gen/reliant/v1/chat_pb.js";
import { StreamingService } from "#gen/reliant/v1/streaming_pb.js";
import { ChatSession, type ChatSessionOptions } from "./chat-session.js";
import { ChatStream, type ChatStreamOptions } from "./chat-stream.js";
import { noopLogger, type Logger } from "./logger.js";
import {
  ProcessOutputStream,
  type ProcessOutputStreamOptions,
} from "./process-output-stream.js";
import type { StreamEnvironment } from "./reconnecting-stream.js";
import { resolveTransport, type ReliantTransportOptions } from "./transport.js";
import { UserStream, type UserStreamOptions } from "./user-stream.js";

export interface ReliantClientOptions
  extends ReliantTransportOptions,
    Pick<StreamEnvironment, "wake" | "onConnectionLost" | "tuning"> {
  /** Where the SDK writes diagnostics. Silent by default. */
  logger?: Logger;
}

type Own<O, K extends keyof O = never> = Omit<O, keyof StreamEnvironment | "client" | K>;

export interface ReliantClient {
  /** The Connect transport every call and stream of this client rides on. */
  readonly transport: Transport;
  /** A typed client for one Reliant service on that transport (what the React hooks call through). */
  service<S extends DescService>(service: S): Client<S>;
  /** Open a chat: its stream, its state, and sends. */
  chat(options: Omit<ChatSessionOptions, "chatId" | "env" | "chats" | "streaming"> & { chatId: string }): ChatSession;
  readonly streams: {
    /** The user-wide update stream. */
    user(options: Own<UserStreamOptions>): UserStream;
    /** One chat's detail stream, switchable between chats. Prefer `chat()` unless you keep your own state. */
    chat(options: Own<ChatStreamOptions>): ChatStream;
    /** The live output of a background process. */
    processOutput(options: Own<ProcessOutputStreamOptions>): ProcessOutputStream;
  };
}

export function createReliantClient(options: ReliantClientOptions): ReliantClient {
  const transport = resolveTransport(options);
  const logger = options.logger ?? noopLogger;
  const env: StreamEnvironment = {
    logger,
    wake: options.wake,
    onConnectionLost: options.onConnectionLost,
    tuning: options.tuning,
  };
  // Resolved per call: the transport may route by procedure and follow a changing base URL.
  const streaming = () => createClient(StreamingService, transport);
  const background = () => createClient(BackgroundService, transport);
  const chats = () => createClient(ChatService, transport);

  return {
    transport,
    service: (service) => createClient(service, transport),
    chat: (o) => new ChatSession({ ...o, env, chats, streaming }),
    streams: {
      user: (o) => new UserStream({ ...env, ...o, client: streaming }),
      chat: (o) => new ChatStream({ ...env, ...o, client: streaming }),
      processOutput: (o) => new ProcessOutputStream({ ...env, ...o, client: background }),
    },
  };
}
