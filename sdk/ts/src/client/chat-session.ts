// ChatSession: one chat, live. It owns the chat's stream, folds every frame
// into a ChatState with the pure reducer, and sends messages with ids that
// make a retry safe. Any framework binds to it through subscribe/getState.

import { ConnectError, Code, type Client } from "@connectrpc/connect";
import { MessageRole, type ChatService } from "#gen/reliant/v1/chat_pb.js";
import { createChatState, reduceChat } from "./chat-reducer.js";
import type { ChatAction, ChatState } from "./chat-state.js";
import { ChatStream, type ChatStreamHooks } from "./chat-stream.js";
import type { ConnectionStatus, ErrorUpdate } from "./events.js";
import type { StreamEnvironment } from "./reconnecting-stream.js";
import { oldestSeqOf } from "./reducers.js";
import { createAttemptIds, clientIdOfFailedSend, type AttemptIds } from "./send-ids.js";
import type { Message } from "./types.js";
import type { StreamingClient } from "./user-stream.js";

export interface ChatSessionOptions {
  chatId: string;
  /**
   * State to resume from, e.g. one the host persisted. Its cursor is where the
   * stream asks the server to replay from; without it the server sends a snapshot.
   */
  initialState?: ChatState;
  /** The project the user stream is scoped to. */
  projectId?: string;
  hooks?: ChatStreamHooks;
  /** Reducer clock; default is the wall clock. */
  now?: () => string;
  /** Ids for sends. Default: a fresh set for this session. */
  attemptIds?: AttemptIds;
  /** Sleep between send retries; injectable for tests. */
  sleep?: (ms: number) => Promise<void>;
  /** @internal set by createReliantClient. */
  env: StreamEnvironment;
  /** @internal */
  chats: () => Client<typeof ChatService>;
  /** @internal */
  streaming: () => StreamingClient;
}

export interface SendOptions {
  attachments?: string[];
  /** Send to a spawned thread instead of the main one. */
  targetThread?: string;
  /** Transient failures retried under the same id before the send is reported failed. Default 3. */
  retries?: number;
}

export interface SendResult {
  messageId: string;
  /** The message was queued for a running thread rather than saved to the transcript. */
  queued: boolean;
  runId: string;
  clientMessageId: string;
}

/** One send's request, as it was first made: what a retry of it repeats. */
interface PendingSend {
  text: string;
  attachments: string[];
  targetThread: string | undefined;
  /** The send's key in the id cache. */
  key: string;
}

/** Codes a repeat of the same send can succeed after. */
const TRANSIENT = new Set<Code>([Code.Unavailable, Code.DeadlineExceeded, Code.Unknown, Code.Aborted, Code.ResourceExhausted]);
const isTransient = (error: unknown): boolean => !(error instanceof ConnectError) || TRANSIENT.has(error.code);

export class ChatSession {
  readonly chatId: string;
  private state: ChatState;
  private status: ConnectionStatus = "disconnected";
  private readonly listeners = new Set<() => void>();
  private readonly stream: ChatStream;
  private readonly now: () => string;
  private readonly ids: AttemptIds;
  private readonly sleep: (ms: number) => Promise<void>;
  private readonly chats: () => Client<typeof ChatService>;
  /** Failed sends by client message id, until retried or discarded. */
  private readonly failedSends = new Map<string, PendingSend>();
  private errorMessage: string | null = null;
  private closed = false;
  private olderInFlight = false;
  private started = false;

  constructor(options: ChatSessionOptions) {
    this.chatId = options.chatId;
    this.now = options.now ?? (() => new Date().toISOString());
    this.ids = options.attemptIds ?? createAttemptIds();
    this.sleep = options.sleep ?? ((ms) => new Promise((resolve) => setTimeout(resolve, ms)));
    this.chats = options.chats;
    this.state = options.initialState ?? createChatState(options.chatId);

    const resumeFrom = options.initialState?.cursor ?? 0n;
    this.stream = new ChatStream({
      ...options.env,
      client: options.streaming,
      projectId: options.projectId,
      hooks: options.hooks,
      callbacks: {
        onChatSnapshot: (_chatId, updates) =>
          this.dispatch({ kind: "snapshot", updates, cursor: this.cursor() }),
        onChatUpdate: (_chatId, updates) =>
          this.dispatch({ kind: "updates", updates, cursor: this.cursor() }),
        onChatPaginationInfo: (_chatId, pagination) => this.dispatch({ kind: "pagination", pagination }),
        onChatContextUsage: (_chatId, contextUsage) => this.dispatch({ kind: "contextUsage", contextUsage }),
        onChatSyncPending: (pendingFor) => {
          if (pendingFor !== null) {
            this.state = { ...this.state, syncPending: true };
            this.emit();
          } else {
            this.dispatch({ kind: "caughtUp", cursor: this.cursor() });
          }
        },
        onStatusChange: (status) => {
          this.status = status;
          if (status === "connected") this.errorMessage = null;
          this.emit();
        },
        onError: (message) => {
          this.errorMessage = message;
          this.emit();
        },
        resolveChatResumeSequence: () => resumeFrom,
        onChatSubscriptionRejected: () => {
          this.errorMessage = "The server refused this chat (it does not exist, or is not yours).";
          this.emit();
        },
      },
    });
  }

  /** Begin streaming. Idempotent. */
  start(): this {
    if (this.closed || this.started) return this;
    this.started = true;
    this.stream.subscribeToChat(this.chatId);
    return this;
  }

  /** Stop streaming for good. The state stays readable. */
  close(): void {
    this.closed = true;
    this.stream.stop();
    this.status = "disconnected";
    this.emit();
  }

  /** The chat as the stream has built it. A new object on every change, so it suits `useSyncExternalStore`. */
  getState = (): ChatState => this.state;

  getConnectionStatus = (): ConnectionStatus => this.status;

  /** The last stream error, cleared when the stream is connected again. */
  getError = (): string | null => this.errorMessage;

  subscribe = (listener: () => void): (() => void) => {
    this.listeners.add(listener);
    return () => {
      this.listeners.delete(listener);
    };
  };

  /** Feed the reducer an action of your own, e.g. a discarded failed send. */
  dispatch(action: ChatAction): void {
    if (action.kind === "sendDiscarded") this.failedSends.delete(action.clientMessageId);
    this.state = reduceChat(this.state, action, { now: this.now });
    this.emit();
  }

  /**
   * Send a user message. The id belongs to the logical send (same text, same
   * attachments, same thread): a failed attempt keeps it, and sending that text
   * again reuses it, so a response lost on the way back saves the message once.
   * Transient failures are retried under that id before the send is reported
   * failed; the optimistic entry then stays, marked, until it is sent again or
   * discarded.
   */
  async send(text: string, options: SendOptions = {}): Promise<SendResult> {
    const request: PendingSend = {
      text,
      attachments: options.attachments ?? [],
      targetThread: options.targetThread,
      key: "",
    };
    request.key = JSON.stringify([this.chatId, request.targetThread ?? "", text, request.attachments]);
    return this.deliver(this.ids.idFor(request.key), request, options.retries);
  }

  /**
   * Send a failed entry again: the exact request that failed (its text,
   * attachments and thread) under the id it already has, however many other
   * sends came between. `options` fill in only an entry this session did not
   * send itself (one restored from a persisted state); its `retries` always applies.
   */
  async retry(failedMessageId: string, options: SendOptions = {}): Promise<SendResult> {
    const clientMessageId = clientIdOfFailedSend(failedMessageId);
    const entry = this.state.messages.find((m) => m.id === failedMessageId);
    if (!clientMessageId || !entry) throw new Error(`${failedMessageId} is not a failed send of this chat`);
    const request: PendingSend = this.failedSends.get(clientMessageId) ?? {
      text: entry.contentBlocks.map((b) => b.content ?? "").join(""),
      attachments: options.attachments ?? [],
      targetThread: options.targetThread,
      key: "",
    };
    if (!request.key) {
      request.key = JSON.stringify([this.chatId, request.targetThread ?? "", request.text, request.attachments]);
    }
    return this.deliver(clientMessageId, request, options.retries);
  }

  private async deliver(clientMessageId: string, request: PendingSend, retries = 3): Promise<SendResult> {
    const { text, attachments, targetThread, key } = request;
    // A retry of a failed send replaces its "Not sent" entry.
    this.dispatch({ kind: "sendDiscarded", clientMessageId });
    this.dispatch({ kind: "sendStarted", clientMessageId, text, createdAt: this.now() });

    const attempts = 1 + retries;
    for (let attempt = 1; ; attempt++) {
      try {
        const response = await this.chats().sendMessage({
          chatId: this.chatId,
          clientMessageId,
          messages: [{ role: MessageRole.USER, content: text }],
          attachments,
          targetThread,
        });
        this.ids.end(key);
        // A queued message is in the thread's mailbox, not the transcript: it
        // has no row to replace its optimistic entry, so the entry goes.
        if (response.queued) this.dispatch({ kind: "sendDiscarded", clientMessageId });
        return {
          messageId: response.messageId,
          queued: response.queued,
          runId: response.runId,
          clientMessageId,
        };
      } catch (error) {
        if (attempt < attempts && isTransient(error)) {
          await this.sleep(Math.min(500 * 2 ** (attempt - 1), 4_000));
          continue;
        }
        // Kept with its id, outside the bounded id cache, so retry() resends
        // exactly this request under exactly this id.
        this.failedSends.set(clientMessageId, request);
        this.dispatch({ kind: "sendFailed", clientMessageId });
        throw error;
      }
    }
  }

  /** Stop the work in flight on a thread; its tool calls are cancelled. */
  async interrupt(threadId: string = this.chatId): Promise<{ cancelledToolCalls: number; undeliverableToolCalls: string[] }> {
    const response = await this.chats().interruptThread({ chatId: this.chatId, threadId });
    return { cancelledToolCalls: response.cancelledToolCalls, undeliverableToolCalls: response.undeliverableToolCalls };
  }

  /** Pause the chat's run. It stays resumable. */
  async pause(): Promise<void> {
    await this.chats().pauseChat({ chatId: this.chatId });
  }

  /** Resume a paused chat's run. */
  async resume(): Promise<void> {
    await this.chats().resumeChat({ chatId: this.chatId });
  }

  /**
   * Fetch the page of history just older than what the chat holds and prepend
   * it. Resolves to whether more remains (the server's word, or a full page).
   * One page at a time: a call while one
   * is in flight resolves to true without a second request.
   */
  async loadOlder(pageSize = 100): Promise<boolean> {
    if (this.olderInFlight) return true;
    const pagination = this.state.pagination;
    // Nothing held yet, or the whole history is: there is nothing to page from.
    if (!pagination || !pagination.hasMore) return false;
    const beforeSeq = oldestSeqOf(this.state.messages);
    if (beforeSeq <= 0) return false;
    this.olderInFlight = true;
    this.emit();
    try {
      const response = await this.chats().listMessages({ chatId: this.chatId, recent: pageSize, beforeSeq: BigInt(beforeSeq) });
      const messages = response.messages as unknown as Message[];
      // An empty page is the start of history. Otherwise the server says whether
      // more lies below the cursor; a full page is more evidence of it, so an
      // answer that under-reports does not strand the older history.
      const hasMore = messages.length > 0 && (response.hasMore || messages.length >= pageSize);
      const errors = response.errors.flatMap((e): ErrorUpdate[] => {
        try {
          const data = JSON.parse(e.dataJson || "{}") as Omit<ErrorUpdate, "sequence_number">;
          return [{ ...data, update_type: "error", sequence_number: Number(e.sequenceNumber) }];
        } catch {
          return [];
        }
      });
      this.dispatch({ kind: "olderPage", messages, errors, total: response.total, hasMore });
      return hasMore;
    } finally {
      this.olderInFlight = false;
      this.emit();
    }
  }

  /** Whether an older page is being fetched. */
  isLoadingOlder = (): boolean => this.olderInFlight;

  private cursor(): bigint {
    return BigInt(this.stream.getLastChatSequence());
  }

  private emit(): void {
    for (const listener of [...this.listeners]) listener();
  }
}
