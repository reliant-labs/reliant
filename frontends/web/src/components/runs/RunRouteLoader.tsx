// Copyright (c) 2025 Reliant Labs

/**
 * Loads what a run's transcript needs before it renders (WORKFLOW_UI.md §4.2,
 * "Reuse, don't fork"): the chat, its project and worktree selected in the
 * stores ChatContainer reads, and the live update stream connected.
 *
 * The update stream normally connects from ModernApp once chats have loaded.
 * A run opened cold — a pasted link, a notification, a refresh — never mounts
 * ModernApp, so this connects it, after the project selection has loaded
 * chats (connect() defers until it has). Connecting twice is a no-op.
 */

import { useEffect, useState, type ReactNode } from "react";
import { Code, ConnectError } from "@connectrpc/connect";

import type { Chat } from "@/api/client";
import { useGlobalUpdatesStore } from "@/store/globalUpdatesStore";
import { loadRunContext, RunProjectUnavailableError } from "./loadRunContext";

export type RunLoadState =
  | { status: "loading" }
  | { status: "ready"; chat: Chat }
  | { status: "not_found" }
  | { status: "error"; message: string; retry: () => void };

export function useRunRoute(chatId: string): RunLoadState {
  const [attempt, setAttempt] = useState(0);
  const [state, setState] = useState<RunLoadState>({ status: "loading" });

  useEffect(() => {
    let cancelled = false;
    setState({ status: "loading" });
    loadRunContext(chatId)
      .then((chat) => {
        if (cancelled) return;
        useGlobalUpdatesStore.getState().connect();
        setState({ status: "ready", chat });
      })
      .catch((error: unknown) => {
        if (cancelled) return;
        if (
          (error instanceof ConnectError && (error.code === Code.NotFound || error.code === Code.PermissionDenied)) ||
          error instanceof RunProjectUnavailableError
        ) {
          setState({ status: "not_found" });
          return;
        }
        setState({
          status: "error",
          message: error instanceof Error ? error.message : String(error),
          retry: () => setAttempt((n) => n + 1),
        });
      });
    return () => {
      cancelled = true;
    };
  }, [chatId, attempt]);

  return state;
}

/** Render-prop form, for hosts that only need the ready chat. */
export function RunRouteLoader({
  chatId,
  fallback,
  children,
}: {
  chatId: string;
  fallback: (state: Exclude<RunLoadState, { status: "ready" }>) => ReactNode;
  children: (chat: Chat) => ReactNode;
}) {
  const state = useRunRoute(chatId);
  return <>{state.status === "ready" ? children(state.chat) : fallback(state)}</>;
}
