// Copyright (c) 2026 Reliant Labs

/**
 * One chat's row in the nav's chat list, out of Layout/Sidebar.tsx so a list
 * that is not the app's nav draws the same row: the phone's drawer
 * (Mobile/MobileNavChatList) lists its chats with it. The row is the
 * sidebar's primitive (primitives.tsx ChatRow) with the chat's state, unread,
 * app and launch kind; renaming and the context menu are the host's, and a
 * row with neither has no hover actions.
 */

import type React from "react";
import { memo } from "react";
import { MoreHorizontal } from "lucide-react";

import type { Chat } from "../../../api/client";
import { useTriggerName } from "../../../hooks/trigger-queries";
import { chatListIndicators, launchKindDisplay } from "../../../lib/runStatus";
import { isAdoptedAutomation, isRun } from "../../../lib/sidebarChatList";
import type { ChatActivity, DotState as ChatActivityState } from "../../../store/activityStore";
import { activityNeedsAttention } from "../../../store/chatNavigationStore";
import Tooltip from "../../forge-ui/tooltip";
import { ChatAppTag } from "../../Projects/ChatAppTag";
import { LaunchKindIcon } from "../../runs/LaunchKindIcon";
import { chatRowChip } from "../chatRowStatus";
import { ChatRow, ChatStateSlot, SIDEBAR_ICON_BUTTON } from "./primitives";

/**
 * The small launch-kind icon before an automation row's title (§6.3): this
 * conversation was started by a schedule or an agent, not by you. Adopted
 * runs carry it permanently; a run listed only because it is open carries it
 * too, so the row says why it is there.
 */
function AutomationOriginGlyph({ chat }: { chat: Chat }) {
  const triggerName = useTriggerName(chat.triggerId);
  const launch = launchKindDisplay(chat.launchKind, { triggerName });
  const label = isAdoptedAutomation(chat)
    ? launch.startedByLine
    : `${launch.startedByLine}. Shown while open; reply to keep it in your chats.`;
  return (
    <Tooltip content={label} side="top" delay={300}>
      <span
        role="img"
        aria-label={label}
        className="flex shrink-0 text-muted-foreground"
        data-testid={`chat-origin-glyph-${chat.id}`}
        data-launch-kind={launch.kind}
      >
        <LaunchKindIcon kind={launch.kind} className="h-3 w-3" />
      </span>
    </Tooltip>
  );
}

export interface ChatWithActivity extends Chat {
  /** Coarse classification for sorting, grouping and the unread badge. */
  activityState: ChatActivityState;
  /** The live activity itself; the dot's label and color come from lib/runStatus. */
  liveActivity: ChatActivity;
  /** Threads still working (Chat.working_threads), from activityStore. */
  liveWorkingThreads: number;
  priority: number;
  lastActivity?: string;
}

/** The navigation store's rule, so keyboard triage walks the list as sorted. */
export const chatNeedsAttention = (chat: ChatWithActivity) =>
  activityNeedsAttention(chat.liveActivity, chat.unread);

/** A chat with no title yet. Not "New chat": that is the button above the list. */
export const UNTITLED_CHAT = "Untitled chat";

// Shared utility - avoids duplicate definitions in ChatItem/ArchivedChatItem
export function getRelativeTime(timestamp: string): string {
  const now = new Date();
  const past = new Date(timestamp);
  const diffMs = now.getTime() - past.getTime();
  const diffMins = Math.floor(diffMs / 60000);
  const diffHours = Math.floor(diffMs / 3600000);
  const diffDays = Math.floor(diffMs / 86400000);

  if (diffMins < 60) {
    return diffMins <= 1 ? "now" : `${diffMins}m`;
  } else if (diffHours < 24) {
    return `${diffHours}h`;
  } else {
    return `${diffDays}d`;
  }
}

/**
 * A row's tooltip: its full title, and when it last changed. The row has no
 * time column (restyle §11.4); the time is here.
 */
export function rowTooltip(title: string, chat: Chat): string {
  const relative = getRelativeTime(chat.updatedAt || chat.createdAt);
  return `${title}\nUpdated ${relative === "now" ? "just now" : `${relative} ago`}`;
}

/** The row's hover-only "More options": the context menu, whose first group holds Archive. */
function ChatMoreButton({ title, onOpen }: { title: string; onOpen: (e: React.MouseEvent) => void }) {
  return (
    <Tooltip content="More options" side="left" delay={300} type="label">
      <button
        type="button"
        aria-haspopup="menu"
        aria-label={`More options for ${title}`}
        onClick={onOpen}
        className={SIDEBAR_ICON_BUTTON}
      >
        <MoreHorizontal className="size-4" aria-hidden="true" />
      </button>
    </Tooltip>
  );
}

// --- ChatItem (module-level for stable identity + React.memo) ---

export interface ChatItemProps {
  chat: ChatWithActivity;
  inGroup?: boolean;
  /** The row being renamed in place, if any (the app's nav; a phone's list renames nothing). */
  editingChatId?: string | null;
  editingTitle?: string;
  /** Out of a group (the flat list): the chat's project, when the list spans several. */
  projectName?: string;
  /** Out of a group: the chat's workspace branch, when it is worth naming. */
  workspaceBranch?: string;
  activeChatId: string | null;
  /** The one row in the tab order (the open chat, else the first). */
  isTabStop?: boolean;
  onChatClick: (chat: ChatWithActivity) => void;
  /** The row's actions (right-click, and the hover-only "More options"); none without it. */
  onContextMenu?: (e: React.MouseEvent, chat: ChatWithActivity) => void;
  onEditingTitleChange?: (title: string) => void;
  onSaveRename?: (chatId: string) => void;
  onCancelRename?: () => void;
}

export const ChatItem = memo(function ChatItem({
  chat,
  inGroup = false,
  editingChatId = null,
  editingTitle: propsEditingTitle = "",
  projectName,
  workspaceBranch,
  activeChatId,
  isTabStop = false,
  onChatClick,
  onContextMenu,
  onEditingTitleChange,
  onSaveRename,
  onCancelRename,
}: ChatItemProps) {
  const isActive = activeChatId === chat.id;
  // Needs-input, the run status and unread are separate indicators: a chat
  // can need an answer while other agents keep running, and either is a
  // different thing from having unread messages.
  const { needsInput, status: runStatus, unread } = chatListIndicators({
    activity: chat.liveActivity,
    workingThreads: chat.liveWorkingThreads,
    unread: chat.unread,
  });
  const state = chatRowChip({ needsInput, status: runStatus });
  const chatTitle = chat.title || UNTITLED_CHAT;
  const isEditing = editingChatId === chat.id;

  const editor = isEditing ? (
    <input
      type="text"
      aria-label="Chat title"
      value={propsEditingTitle}
      onChange={(e) => onEditingTitleChange?.(e.target.value)}
      onKeyDown={(e) => {
        if (e.key === "Enter") {
          e.preventDefault();
          onSaveRename?.(chat.id);
        } else if (e.key === "Escape") {
          e.preventDefault();
          onCancelRename?.();
        }
      }}
      onBlur={() => onSaveRename?.(chat.id)}
      onClick={(e) => e.stopPropagation()}
      autoFocus
      className="h-7 min-w-0 flex-1 rounded-md border border-ring bg-background px-2 text-ui focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
    />
  ) : undefined;

  // Out of a group, the row says where the chat is, after its title: the
  // project, then the branch (restyle §11.5, flat view).
  const where =
    !inGroup && (projectName || workspaceBranch) ? (
      <>
        <span aria-hidden="true">· </span>
        {projectName && <span data-testid={`chat-project-${chat.id}`}>{projectName}</span>}
        {projectName && workspaceBranch && " / "}
        {workspaceBranch}
      </>
    ) : undefined;

  return (
    <ChatRow
      chatId={chat.id}
      title={chatTitle}
      tooltip={rowTooltip(chatTitle, chat)}
      current={isActive}
      unread={unread}
      inProject={inGroup}
      slot={
        <>
          {state && (
            <ChatStateSlot
              chatId={chat.id}
              state={state}
              activityState={chat.activityState}
              runningBeside={!!needsInput && !!runStatus}
            />
          )}
          {/* Unread is the title's weight; this names it for a screen reader. */}
          {unread && (
            <span
              className="sr-only"
              data-testid={`chat-unread-dot-${chat.id}`}
              role="img"
              aria-label="Unread messages"
            />
          )}
        </>
      }
      suffix={where}
      glyph={
        isRun(chat) || chat.appName ? (
          <>
            {chat.appName && <ChatAppTag name={chat.appName} interactive={false} compact className="ml-1" />}
            {isRun(chat) && <AutomationOriginGlyph chat={chat} />}
          </>
        ) : undefined
      }
      editor={editor}
      isTabStop={isTabStop}
      onClick={() => onChatClick(chat)}
      onContextMenu={onContextMenu ? (e) => onContextMenu(e, chat) : undefined}
      hoverActions={!isEditing && onContextMenu && <ChatMoreButton title={chatTitle} onOpen={(e) => onContextMenu(e, chat)} />}
    />
  );
});
