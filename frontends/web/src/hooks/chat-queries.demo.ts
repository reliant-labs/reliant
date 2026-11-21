// Copyright (c) 2026 Reliant Labs

/**
 * hooks/chat-queries.ts in the forge demo's bundle (a demo twin,
 * vite-plugin-demo-bundle.ts): the demo has no chats. The one shell asks for
 * the open chat to find its project (Layout/sidebar/shellProject.ts); in the
 * demo no page is a chat, so the query never runs. The real module reaches
 * the chat store and the session, which the demo bundle must not contain.
 */

import { useQuery } from "@tanstack/react-query";

import type * as Real from "./chat-queries";

export const useChat: typeof Real.useChat = (chatId) =>
  useQuery({
    queryKey: ["chats", "detail", chatId ?? ""],
    queryFn: () => Promise.reject(new Error("Chats run in Reliant, not in the forge demo.")),
    enabled: false,
  }) as ReturnType<typeof Real.useChat>;
