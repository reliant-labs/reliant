// Copyright (c) 2025 Reliant Labs

import { useChatList } from "@/hooks/chat-queries";
import type { Trigger } from "@/hooks/trigger-model";
import type { DaemonChoice } from "../daemonChoices";
import { lastUsedDaemon } from "./machineDefault";

/** Project-scoped history, not the globally selected (possibly unrelated) machine. */
export function useRecentMachine(projectId: string, triggers: readonly Trigger[], choices: readonly DaemonChoice[]) {
  const chats = useChatList(projectId || undefined);
  const available = new Set(choices.filter((c) => c.eligible && c.statusLabel !== "failed").map((c) => c.daemonId));
  const uses = [
    ...(chats.data ?? []).filter((c) => c.projectId === projectId).map((c) => ({ daemonId: c.activeDaemonId, at: c.lastMessageAt ?? c.updatedAt })),
    ...triggers.filter((t) => t.projectId === projectId).map((t) => ({ daemonId: t.daemonId, at: t.updatedAt })),
  ];
  return {
    lastUsed: lastUsedDaemon(uses.filter((use) => !!use.daemonId && available.has(use.daemonId))),
    loading: !!projectId && chats.isLoading,
  };
}
