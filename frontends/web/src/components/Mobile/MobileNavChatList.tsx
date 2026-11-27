// Copyright (c) 2026 Reliant Labs

/**
 * The chat list inside the phone's nav drawer (the nav config's `chat-list`
 * slot; Layout/sidebar/MobileHybridNav): what the shell's drawer shows under
 * Agents on a chat page, so the phone's drawer matches it (owner: "the mobile
 * left nav, on chats, is the old nav").
 *
 * It is NOT the app nav's list (Layout/Sidebar.tsx): that one carries the
 * desktop's state (its project filter's URL sync, `openChat`'s desktop URLs),
 * which must not run on `/m/*`. This reads the phone's own scope
 * (`?projects=`, mobileScope.ts) and draws each chat with the nav's own row
 * (Layout/sidebar/ChatItem), linking to the phone's chat screen. Renaming,
 * the context menu, the archived tab and grouping stay on the phone's chat
 * list screen (MobileChatList).
 */

import { useMemo } from "react";
import { useParams } from "@tanstack/react-router";

import { ChatState } from "../../gen/reliant/v1/chat_pb";
import { useChatListWithWorkspaces } from "../../hooks/chat-queries";
import { useProjects } from "../../hooks/project-queries";
import { sortChats } from "../../lib/chatListOrder";
import { workspaceOfChat } from "../../lib/chatWorkspace";
import { chatListRows } from "../../lib/sidebarChatList";
import { cn } from "../../lib/utils";
import { activityToDotState, ChatActivity, useActivityStore } from "../../store/activityStore";
import { useSortOrder } from "../../store/chatListPreferencesStore";
import { mobileChatPath } from "@/lib/paths";
import { ChatItem, chatNeedsAttention, type ChatWithActivity } from "../Layout/sidebar/ChatItem";
import type { SlotLayout } from "../Layout/sidebar/NavRenderer";
import { useMobileChatScope } from "./mobileScope";

export function MobileNavChatList({ place, depth, onOpen }: SlotLayout & { onOpen: (href: string) => void }) {
  const scope = useMobileChatScope();
  const { data: chatList, isPending, isError } = useChatListWithWorkspaces(scope);
  const { chatId: openChatId } = useParams({ strict: false }) as { chatId?: string };
  const sortOrder = useSortOrder();
  const activities = useActivityStore((state) => state.activities);
  const workingThreads = useActivityStore((state) => state.workingThreads);
  const { projects } = useProjects();
  const projectNames = useMemo(() => new Map(projects.map((p) => [p.id, p.name])), [projects]);

  const chats = useMemo((): ChatWithActivity[] => {
    const rows = chatListRows(chatList?.chats ?? []).filter((chat) => chat.state !== ChatState.ARCHIVED);
    const withActivity = rows.map((chat) => {
      const liveActivity = activities.get(chat.id) ?? ChatActivity.IDLE;
      return {
        ...chat,
        activityState: activityToDotState(liveActivity),
        liveActivity,
        liveWorkingThreads: workingThreads.get(chat.id) ?? 0,
        priority: 0,
        lastActivity: chat.updatedAt,
      };
    });
    return sortChats(withActivity, sortOrder, chatNeedsAttention);
  }, [chatList?.chats, activities, workingThreads, sortOrder]);

  // One project's list names no project on its rows; across several, each row names its own (as the app's nav does).
  const oneProject = new Set(chats.map((chat) => chat.projectId)).size <= 1;

  return (
    <div data-testid="mobile-nav-chat-list" data-place={place} className={place === "fill" ? "flex min-h-0 flex-1 flex-col" : undefined}>
      <div className={cn("flex h-7 shrink-0 items-center gap-1 pr-1 text-meta font-medium text-muted-foreground", depth === 0 ? "mt-3 pl-2" : "mt-1 pl-8")}>
        <span className="min-w-0 flex-1 truncate">Chats</span>
      </div>
      <div className={cn("overflow-y-auto overscroll-contain", place === "fill" ? "min-h-0 flex-1" : "max-h-[45dvh]")}>
        {chats.length > 0 && (
          <div className="space-y-0.5">
            {chats.map((chat, index) => {
              const worktree = workspaceOfChat(chat, chatList?.mainWorktrees);
              return (
                <ChatItem
                  key={chat.id}
                  chat={chat}
                  inGroup
                  projectName={oneProject ? undefined : projectNames.get(chat.projectId)}
                  workspaceBranch={(oneProject || !worktree?.isMain ? worktree?.branch : undefined) || undefined}
                  activeChatId={openChatId ?? null}
                  isTabStop={openChatId ? chat.id === openChatId : index === 0}
                  onChatClick={() => onOpen(mobileChatPath(chat.id))}
                />
              );
            })}
          </div>
        )}
        {isPending && (
          <div role="status" aria-label="Loading chats" className="space-y-1.5 py-2 pl-8 pr-1">
            {Array.from({ length: 4 }, (_, i) => (
              <div key={i} className="h-7 animate-pulse rounded-md bg-foreground/5 motion-reduce:animate-none" />
            ))}
          </div>
        )}
        {isError && <p className="py-1 pl-8 pr-2 text-meta text-muted-foreground">Couldn't load your chats</p>}
        {!isPending && !isError && chats.length === 0 && <p className="py-1 pl-8 pr-2 text-meta text-muted-foreground">No chats yet</p>}
      </div>
    </div>
  );
}
