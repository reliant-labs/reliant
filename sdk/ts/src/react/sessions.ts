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

// A session's stream is scoped by the project it was opened with, so the same
// chat under two scopes is two sessions: sharing one would silently route the
// second caller's stream by the first caller's project.
const keyOf = (chatId: string, projectId: string | undefined): string => `${chatId}\u0000${projectId ?? ""}`;

function entryFor(client: ReliantClient, key: string, chatId: string, projectId: string | undefined): Entry {
  let byKey = registry.get(client);
  if (!byKey) {
    byKey = new Map();
    registry.set(client, byKey);
  }
  let entry = byKey.get(key);
  if (!entry) {
    entry = { session: client.chat({ chatId, projectId }), refs: 0, closing: false };
    byKey.set(key, entry);
  }
  return entry;
}

function release(client: ReliantClient, key: string, entry: Entry): void {
  entry.refs--;
  if (entry.refs > 0) return;
  entry.closing = true;
  queueMicrotask(() => {
    if (!entry.closing) return;
    entry.session.close();
    const byKey = registry.get(client);
    if (byKey?.get(key) === entry) byKey.delete(key);
  });
}

/**
 * The live session of a chat under a project scope, streaming for as long as
 * the component is mounted. Hooks that pass the same chat and project share one. `enabled: false` (or no chat id) holds none: the chat is not read.
 */
export function useChatSession(chatId: string | undefined, projectId?: string, enabled = true): ChatSession | null {
  const client = useReliantClient();
  const key = chatId ? keyOf(chatId, projectId) : "";
  const entry = chatId && enabled ? entryFor(client, key, chatId, projectId) : null;

  useEffect(() => {
    if (!entry) return;
    entry.refs++;
    entry.closing = false;
    entry.session.start();
    return () => release(client, key, entry);
  }, [client, key, entry]);

  return entry?.session ?? null;
}
