"use client";
// One live ChatSession per (client, chat), shared by every hook that reads the
// chat. Two hooks on the same chat are one stream and one state, not two.
//
// A session is made when first asked for and started when a component mounts
// it. When the last one unmounts it is closed on the next microtask: React's
// StrictMode unmounts and remounts an effect in the same tick, and that must
// not cost a stream (nor the state, nor the cursor).

import { useEffect } from "react";
import type { ReliantClient } from "../client/client.js";
import type { ChatSession } from "../client/chat-session.js";
import { useReliantClient } from "./context.js";

interface Entry {
  session: ChatSession;
  refs: number;
  closing: boolean;
}

const registry = new WeakMap<ReliantClient, Map<string, Entry>>();

function entryFor(client: ReliantClient, chatId: string, projectId: string | undefined): Entry {
  let byChat = registry.get(client);
  if (!byChat) {
    byChat = new Map();
    registry.set(client, byChat);
  }
  let entry = byChat.get(chatId);
  if (!entry) {
    entry = { session: client.chat({ chatId, projectId }), refs: 0, closing: false };
    byChat.set(chatId, entry);
  }
  return entry;
}

function release(client: ReliantClient, chatId: string, entry: Entry): void {
  entry.refs--;
  if (entry.refs > 0) return;
  entry.closing = true;
  queueMicrotask(() => {
    if (!entry.closing) return;
    entry.session.close();
    const byChat = registry.get(client);
    if (byChat?.get(chatId) === entry) byChat.delete(chatId);
  });
}

/**
 * The live session of a chat, streaming for as long as the component is
 * mounted. `enabled: false` (or no chat id) holds none: the chat is not read.
 */
export function useChatSession(chatId: string | undefined, projectId?: string, enabled = true): ChatSession | null {
  const client = useReliantClient();
  const entry = chatId && enabled ? entryFor(client, chatId, projectId) : null;

  useEffect(() => {
    if (!entry || !chatId) return;
    entry.refs++;
    entry.closing = false;
    entry.session.start();
    return () => release(client, chatId, entry);
  }, [client, chatId, entry]);

  return entry?.session ?? null;
}
