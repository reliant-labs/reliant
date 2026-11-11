"use client";
// useChat / useThread: a chat, live. Built on ChatSession (the stream and the
// reducer) through useSyncExternalStore, so every render sees one consistent
// state and a hook costs nothing while the chat is quiet.

import { useCallback, useMemo, useSyncExternalStore } from "react";
import type { ChatSession, SendOptions, SendResult } from "../client/chat-session.js";
import type { ChatState } from "../client/chat-state.js";
import { createChatState } from "../client/chat-reducer.js";
import type { ActiveThreadUpdate, ConnectionStatus, QuestionUpdate, ToolApprovalUpdate } from "../client/events.js";
import { normalizeThreadKey } from "../client/reducers.js";
import type { Message } from "../client/types.js";
import { useChatSession } from "./sessions.js";

export interface UseChatOptions {
  /** Scope the user-wide stream to this project. */
  projectId?: string;
  /** Hold nothing and read nothing while false. Default true. */
  enabled?: boolean;
}

export interface UseChatResult {
  /** The whole state the stream has built; the fields below are views of it. */
  state: ChatState;
  /** Persisted messages in canonical order, plus the optimistic sends. */
  messages: Message[];
  /** The in-flight assistant message of each thread, by thread key. */
  streaming: Readonly<Record<string, Message>>;
  threads: ActiveThreadUpdate[];
  pendingApprovals: ToolApprovalUpdate[];
  /** Questions an agent is blocked on. */
  pendingQuestions: QuestionUpdate[];
  status: ConnectionStatus;
  /** The last stream error, cleared when the stream connects again. */
  error: string | null;
  /** Between a subscription and the server's caught-up: what is shown may still be filling in. */
  syncPending: boolean;
  /** History exists beyond what is held. */
  hasMore: boolean;
  loadingOlder: boolean;
  send(text: string, options?: SendOptions): Promise<SendResult>;
  retry(failedMessageId: string, options?: SendOptions): Promise<SendResult>;
  interrupt(threadId?: string): ReturnType<ChatSession["interrupt"]>;
  pause(): Promise<void>;
  resume(): Promise<void>;
  loadOlder(pageSize?: number): Promise<boolean>;
}

const noop = () => () => {};

function useSessionSnapshot(session: ChatSession | null) {
  const subscribe = session ? session.subscribe : noop;
  const state = useSyncExternalStore(
    subscribe,
    session ? session.getState : () => IDLE_STATE,
    () => IDLE_STATE,
  );
  const status = useSyncExternalStore(
    subscribe,
    session ? session.getConnectionStatus : () => "disconnected" as ConnectionStatus,
    () => "disconnected" as ConnectionStatus,
  );
  const error = useSyncExternalStore(subscribe, session ? session.getError : () => null, () => null);
  const loadingOlder = useSyncExternalStore(subscribe, session ? session.isLoadingOlder : () => false, () => false);
  return { state, status, error, loadingOlder };
}

const IDLE_STATE = createChatState("", { syncPending: false });

function unavailable<T>(): Promise<T> {
  return Promise.reject(new Error("@reliantlabs/sdk/react: the chat is not enabled (no chat id)"));
}

export function useChat(chatId: string | undefined, options: UseChatOptions = {}): UseChatResult {
  const session = useChatSession(chatId, options.projectId, options.enabled ?? true);
  const { state, status, error, loadingOlder } = useSessionSnapshot(session);

  const pendingApprovals = useMemo(() => state.approvals.filter((a) => a.status === "pending"), [state.approvals]);
  const send = useCallback((text: string, o?: SendOptions) => (session ? session.send(text, o) : unavailable<SendResult>()), [session]);
  const retry = useCallback((id: string, o?: SendOptions) => (session ? session.retry(id, o) : unavailable<SendResult>()), [session]);
  const interrupt = useCallback((threadId?: string) => (session ? session.interrupt(threadId) : unavailable<Awaited<ReturnType<ChatSession["interrupt"]>>>()), [session]);
  const pause = useCallback(() => (session ? session.pause() : unavailable<void>()), [session]);
  const resume = useCallback(() => (session ? session.resume() : unavailable<void>()), [session]);
  const loadOlder = useCallback((pageSize?: number) => (session ? session.loadOlder(pageSize) : unavailable<boolean>()), [session]);

  return {
    state,
    messages: state.messages,
    streaming: state.streaming,
    threads: state.threads,
    pendingApprovals,
    pendingQuestions: state.pendingQuestions,
    status,
    error,
    syncPending: state.syncPending,
    hasMore: state.pagination?.hasMore ?? false,
    loadingOlder,
    send,
    retry,
    interrupt,
    pause,
    resume,
    loadOlder,
  };
}

export interface UseThreadResult {
  /** The thread's persisted messages, canonical order. */
  messages: Message[];
  /** The thread's in-flight assistant message, if one is streaming. */
  streaming: Message | null;
  /** The thread's record (title, agent, status), once the stream has named it. */
  thread: ActiveThreadUpdate | null;
  /** The thread is running (a message was sent and not yet answered, or a turn is streaming). */
  active: boolean;
  status: ConnectionStatus;
  syncPending: boolean;
  /** Send to this thread (the main thread when `threadId` is the chat's own id). */
  send(text: string, options?: Omit<SendOptions, "targetThread">): Promise<SendResult>;
  interrupt(): ReturnType<ChatSession["interrupt"]>;
}

/** One thread of a chat: the main thread (the chat's own id) or a spawned sub-agent's. */
export function useThread(chatId: string | undefined, threadId: string | undefined, options: UseChatOptions = {}): UseThreadResult {
  const session = useChatSession(chatId, options.projectId, options.enabled ?? true);
  const { state, status } = useSessionSnapshot(session);

  const key = chatId ? normalizeThreadKey(chatId, threadId) : "";
  const isMain = !!chatId && key === chatId;

  const messages = useMemo(
    () => (chatId ? state.messages.filter((m) => normalizeThreadKey(chatId, m.thread) === key) : []),
    [state.messages, chatId, key],
  );
  const streaming = state.streaming[key] ?? null;
  const thread = useMemo(() => state.threads.find((t) => t.thread === key) ?? null, [state.threads, key]);
  const active = streaming !== null || thread?.status === "running" || thread?.status === "active";

  const send = useCallback(
    (text: string, o?: Omit<SendOptions, "targetThread">) =>
      session ? session.send(text, { ...o, targetThread: isMain ? undefined : key }) : unavailable<SendResult>(),
    [session, isMain, key],
  );
  const interrupt = useCallback(() => (session ? session.interrupt(key) : unavailable<Awaited<ReturnType<ChatSession["interrupt"]>>>()), [session, key]);

  return { messages, streaming, thread, active, status, syncPending: state.syncPending, send, interrupt };
}
