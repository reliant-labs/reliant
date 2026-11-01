import React, {
  useState,
  useMemo,
  useCallback,
  useEffect,
  type ReactNode,
  useRef,
  memo,
} from "react";
import { ChatState } from "../../gen/reliant/v1/chat_pb";
import type { WorktreeSummary } from "../../gen/reliant/v1/worktree_pb";
import { workspaceOfChat } from "../../lib/chatWorkspace";
import { activeWorkspaceChats, groupByProject, groupByWorkspace, type WorkspaceGroup } from "../../lib/sidebarGroups";
import {
  Plus,
  Trash2,
  Archive,
  ChevronDown,
  ChevronRight,
  Edit,
  Copy,
  MoreVertical,
  RotateCcw,
  Mail,
  FolderGit2,
  LayoutList,
  LayoutGrid,
  Clock,
  ArrowDownWideNarrow,
  ArrowUpWideNarrow,
  SortAsc,
  SortDesc,
  Bell,
  GitBranch,
  Folder,
  MessageSquare,
  Search,
  Settings,
  Workflow,
  Boxes,
  Activity,
  MessageCircleQuestion,
} from "lucide-react";
import { useChatStore } from "../../store/chatStore";
import { useChat, useChatListWithWorkspaces, useArchivedChats, useDeleteChat, useRenameChat, useUnarchiveChat } from "../../hooks/chat-queries";
import { useAdoptRun, useUnadoptRun } from "../../hooks/run-queries";
import { useTriggerName } from "../../hooks/trigger-queries";
import { isAdoptedAutomation, isRun, withOpenChatPinned } from "../../lib/sidebarChatList";
import { useSidebarPinStore } from "../../store/sidebarPinStore";
import { LaunchKindIcon } from "../runs/LaunchKindIcon";
import { AutomationActivityPill } from "./AutomationActivityPill";
import { InboxNavItem } from "../inbox/InboxNavItem";
import { SidebarApps } from "../Apps/SidebarApps";
import { OrgSwitcher } from "../Orgs/OrgSwitcher";
import type { RunsSearch } from "../../routeSchemas";
import { isForgeUIEnabled } from "../../lib/forgeFeature";
import { useMarkUnread } from "../../hooks/message-queries";
import { activityNeedsAttention } from "../../store/chatNavigationStore";
import { useProjectStore } from "../../store/projectStore";
import { useWorkspaceStateStore } from "../../store/workspaceStateStore";
import { openChat, openNewChat } from "../../store/openChat";
import { openAddProject } from "../../store/addProjectDialogStore";
import { useProjects } from "../../hooks/project-queries";
import { ProjectFilterMenu } from "./ProjectFilterMenu";
import { ProjectMark } from "./ProjectMark";
import { useShortcutLabel } from "../../store/shortcutsStore";
import { cn } from "../../lib/utils";
import { toast } from "../../lib/toast-manager";
import Tooltip from "../forge-ui/tooltip";
import Button from "@/components/forge-ui/button";
import ContextMenu from "../forge-ui/context_menu";
import type { MenuGroup } from "../forge-ui/dropdown_menu";
import { contextMenuOpening, type ContextMenuOpening } from "../../lib/contextMenuOpening";
import { focusChatInput, isInputFocused } from "../../hooks/useFocusManager";
import type { Chat } from "../../api/client";
import {
  useChatListPreferencesStore,
  type ChatSortOption,
} from "../../store/chatListPreferencesStore";
import { sortChats, compareChatGroups } from "../../lib/chatListOrder";
import DropdownMenu from "../forge-ui/dropdown_menu";
import { RunStatusDot } from "../runs/RunStatusIndicator";
import { chatListIndicators, launchKindDisplay } from "../../lib/runStatus";
import {
  useActivityStore,
  activityToDotState,
  ChatActivity,
  type DotState as ChatActivityState,
} from "../../store/activityStore";

import { archiveWorktree } from "../../hooks/worktree-actions";

const CHAT_HEADER_ACTION_BUTTON_CLASS =
  "flex h-7 w-7 shrink-0 items-center justify-center rounded-md transition-colors focus:outline-none focus:ring-2 focus:ring-ring/40";
const CHAT_HEADER_ACTION_ICON_CLASS = "h-4 w-4 shrink-0";
const CHAT_HEADER_ACTION_TOOLTIP_CLASS = "flex shrink-0";

// Sort options configuration
const SORT_OPTIONS: {
  value: ChatSortOption;
  label: string;
  icon: React.ReactNode;
}[] = [
  {
    value: "recent_activity",
    label: "Recent Activity",
    icon: <Clock className={CHAT_HEADER_ACTION_ICON_CLASS} />,
  },
  {
    value: "needs_attention_first",
    label: "Needs Attention",
    icon: <Bell className={CHAT_HEADER_ACTION_ICON_CLASS} />,
  },
  {
    value: "newest_first",
    label: "Newest First",
    icon: <ArrowDownWideNarrow className={CHAT_HEADER_ACTION_ICON_CLASS} />,
  },
  {
    value: "oldest_first",
    label: "Oldest First",
    icon: <ArrowUpWideNarrow className={CHAT_HEADER_ACTION_ICON_CLASS} />,
  },
  {
    value: "alphabetical_asc",
    label: "A → Z",
    icon: <SortAsc className={CHAT_HEADER_ACTION_ICON_CLASS} />,
  },
  {
    value: "alphabetical_desc",
    label: "Z → A",
    icon: <SortDesc className={CHAT_HEADER_ACTION_ICON_CLASS} />,
  },
];

interface SidebarProps {
  paddingClass?: string;
  /** Open the Workflows area (Workflows · Runs). */
  onOpenWorkflows?: () => void;
  /** Open the Runs tab with filters; the footer pill passes "live". */
  onOpenRuns?: (search?: RunsSearch) => void;
  onOpenChatSearch?: () => void;
  onNavigateToSettings?: () => void;
  /** Settings → Projects, where projects are renamed and removed. */
  onManageProjects?: () => void;
  onOpenForge?: () => void;
  onOpenInbox?: () => void;
}

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

interface SidebarNavButtonProps {
  icon: ReactNode;
  label: string;
  onClick?: () => void;
  testId?: string;
  onboardingId?: string;
  /** The action's shortcut, formatted (useShortcutLabel); shown at the end. */
  shortcut?: string;
}

function SidebarNavButton({ icon, label, onClick, testId, onboardingId, shortcut }: SidebarNavButtonProps) {
  return (
    <button
      type="button"
      onClick={onClick}
      disabled={!onClick}
      className={cn(
        "flex h-8 w-full items-center gap-2 rounded-md px-2 text-left text-sm transition-colors",
        "text-foreground/85 hover:bg-muted/50 hover:text-foreground",
        "focus:outline-none focus:ring-2 focus:ring-ring/40",
        "disabled:cursor-default disabled:opacity-50 disabled:hover:bg-transparent"
      )}
      data-testid={testId}
      data-onboarding={onboardingId}
      // The label alone names the button; the key cap is a visual hint.
      aria-label={label}
    >
      <span className="flex h-4 w-4 shrink-0 items-center justify-center text-muted-foreground">
        {icon}
      </span>
      <span className="min-w-0 truncate">{label}</span>
      {shortcut && (
        <kbd aria-hidden="true" className="ml-auto shrink-0 font-mono text-xs text-muted-foreground">
          {shortcut}
        </kbd>
      )}
    </button>
  );
}

// Workspace group structure (lib/sidebarGroups)
type ChatGroup = WorkspaceGroup<ChatWithActivity>;

// Archived worktree group structure
interface ArchivedChatGroup {
  worktreeName: string; // Display name (may be "Unknown Workspace" if missing)
  worktreeId?: string; // Original worktree ID if available
  chats: ChatWithActivity[];
  isWorktreeArchived: boolean; // True if the worktree itself is archived
  /**
   * Set when the group is a whole project (All projects): `worktreeName` is
   * then the project's name, and each row names its own workspace.
   */
  projectId?: string;
}

interface ChatWithActivity extends Chat {
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
const chatNeedsAttention = (chat: ChatWithActivity) =>
  activityNeedsAttention(chat.liveActivity, chat.unread);

// Shared utility - avoids duplicate definitions in ChatItem/ArchivedChatItem
function getRelativeTime(timestamp: string): string {
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

// --- Extracted ChatItem (module-level for stable identity + React.memo) ---

interface ChatItemProps {
  chat: ChatWithActivity;
  inGroup?: boolean;
  editingChatId: string | null;
  editingTitle: string;
  /** Out of a group (the flat list): the chat's project, when the list spans several. */
  projectName?: string;
  /** Out of a group: the chat's workspace branch, when it is worth naming. */
  workspaceBranch?: string;
  activeChatId: string | null;
  onChatClick: (chat: ChatWithActivity) => void;
  onContextMenu: (e: React.MouseEvent, chat: ChatWithActivity) => void;
  onEditingTitleChange: (title: string) => void;
  onSaveRename: (chatId: string) => void;
  onCancelRename: () => void;
  onArchiveChat: (chatId: string) => void;
}

const ChatItem = memo(function ChatItem({
  chat,
  inGroup = false,
  editingChatId,
  editingTitle: propsEditingTitle,
  projectName,
  workspaceBranch,
  activeChatId,
  onChatClick,
  onContextMenu,
  onEditingTitleChange,
  onSaveRename,
  onCancelRename,
  onArchiveChat,
}: ChatItemProps) {
  const isActive = activeChatId === chat.id;
  // Needs-input, the run dot and unread are separate indicators: a chat can
  // need an answer while other agents keep running, and either is a different
  // thing from having unread messages.
  const { needsInput, status: runStatus, unread } = chatListIndicators({
    activity: chat.liveActivity,
    workingThreads: chat.liveWorkingThreads,
    unread: chat.unread,
  });
  const chatTitle = chat.title || "New chat";
  const isEditing = editingChatId === chat.id;
  const relativeTime = getRelativeTime(chat.updatedAt || chat.createdAt);

  return (
    <div
      data-chat-id={chat.id}
      className={cn(
        "group relative flex w-full cursor-pointer items-center gap-2 overflow-hidden rounded-md border-l-2 border-transparent px-2.5 py-1.5 text-left font-sans text-xs transition-all duration-150",
        isActive
          ? "border-primary bg-primary/10 text-foreground font-semibold"
          : needsInput
            ? "border-warning/40 bg-warning/5 text-foreground hover:bg-warning/10"
            : runStatus
              ? "border-success/30 bg-success/5 text-foreground hover:bg-success/10"
              : "bg-transparent text-foreground/80 hover:bg-muted/50 hover:text-foreground",
        "active:scale-[0.99]"
      )}
      onClick={() => onChatClick(chat)}
      onContextMenu={(e) => onContextMenu(e, chat)}
    >
      {unread && (
        <Tooltip content="Unread messages" side="top" delay={300} triggerClassName="block">
          <div
            className="absolute right-1.5 top-1.5 h-2 w-2 rounded-full bg-destructive ring-2 ring-card"
            data-testid={`chat-unread-dot-${chat.id}`}
            role="img"
            aria-label="Unread messages"
          />
        </Tooltip>
      )}

      {needsInput && (
        <div
          className="relative flex-shrink-0 group-hover:scale-110 transition-transform duration-200"
          data-testid={`chat-needs-input-${chat.id}`}
          data-activity-state={chat.activityState}
        >
          <Tooltip content={needsInput.label} side="left" delay={300}>
            <span role="img" aria-label={needsInput.label} className="flex">
              <MessageCircleQuestion className="h-3.5 w-3.5 text-warning-ink" aria-hidden="true" />
              {/* Agents still running beside the question: the running dot,
                  tucked into the glyph's corner. The label above says it. */}
              {runStatus && (
                <span
                  className="absolute -bottom-1 -right-1 h-2 w-2 rounded-full bg-success ring-2 ring-card animate-pulse motion-reduce:animate-none"
                  data-testid={`chat-needs-input-running-${chat.id}`}
                  aria-hidden="true"
                />
              )}
            </span>
          </Tooltip>
        </div>
      )}

      {!needsInput && runStatus && (
        <div 
          className="flex-shrink-0 group-hover:scale-110 transition-transform duration-200"
          data-testid={`chat-activity-dot-${chat.id}`}
          data-activity-state={chat.activityState}
        >
          <Tooltip content={runStatus.label} side="left" delay={300}>
            <RunStatusDot status={runStatus} size="lg" />
          </Tooltip>
        </div>
      )}

      <div className={cn("flex-1 min-w-0 transition-all duration-200")}>
        {isEditing ? (
          <input
            type="text"
            value={propsEditingTitle}
            onChange={(e) => onEditingTitleChange(e.target.value)}
            onKeyDown={(e) => {
              if (e.key === "Enter") {
                e.preventDefault();
                onSaveRename(chat.id);
              } else if (e.key === "Escape") {
                e.preventDefault();
                onCancelRename();
              }
            }}
            onBlur={() => onSaveRename(chat.id)}
            onClick={(e) => e.stopPropagation()}
            autoFocus
            className="w-full px-2 py-1 bg-background border border-primary rounded text-xs font-mono focus:outline-none focus:ring-2 focus:ring-primary/50"
          />
        ) : (
          <div className="flex min-w-0 items-center gap-1.5">
            {isRun(chat) && <AutomationOriginGlyph chat={chat} />}
            <div className="truncate group-hover:text-foreground transition-colors duration-200">
              {chatTitle}
            </div>
          </div>
        )}
        {!inGroup && (projectName || workspaceBranch) && (
          <ChatWhere chatId={chat.id} projectName={projectName} workspaceBranch={workspaceBranch} />
        )}
      </div>

      <div className="flex-shrink-0 opacity-0 group-hover:opacity-100 transition-opacity duration-200">
        <Tooltip content="More options" side="left" delay={300} type="label">
          <button
            type="button"
            aria-haspopup="menu"
            onClick={(e) => onContextMenu(e, chat)}
            className="hover:bg-muted/70 rounded p-1 flex items-center justify-center"
          >
            <MoreVertical className="w-3 h-3 text-muted-foreground hover:text-foreground transition-colors duration-200" />
          </button>
        </Tooltip>
      </div>

      <div className="flex-shrink-0 relative flex items-center justify-center min-w-[2rem]">
        <span className="text-xs text-muted-foreground group-hover:opacity-0 transition-opacity duration-200">
          {relativeTime}
        </span>
        <Tooltip content="Archive chat" side="left" delay={300}>
          <button
            onClick={(e) => {
              e.stopPropagation();
              onArchiveChat(chat.id);
            }}
            className="opacity-0 group-hover:opacity-100 transition-all duration-200 hover:bg-muted/70 rounded p-0.5 absolute inset-0 flex items-center justify-center"
          >
            <Archive className="w-3 h-3 text-muted-foreground hover:text-foreground transition-colors duration-200" />
          </button>
        </Tooltip>
      </div>
    </div>
  );
});

/**
 * A row's "where", under its title when no header above it says so: the
 * project first (a folder glyph and its name, the stronger of the two), then
 * the workspace branch. Real text, so a screen reader hears it with the title.
 */
function ChatWhere({
  chatId,
  projectName,
  workspaceBranch,
}: {
  chatId: string;
  projectName?: string;
  workspaceBranch?: string;
}) {
  return (
    <div className="mt-0.5 flex min-w-0 items-center gap-1.5 text-2xs text-muted-foreground">
      {projectName && (
        <span className="flex min-w-0 items-center gap-1" data-testid={`chat-project-${chatId}`}>
          <Folder className="h-2.5 w-2.5 shrink-0" aria-hidden="true" />
          <span className="truncate font-medium text-foreground/70">{projectName}</span>
        </span>
      )}
      {workspaceBranch && (
        <span className="flex min-w-0 items-center gap-1">
          <GitBranch className="h-2.5 w-2.5 shrink-0" aria-hidden="true" />
          <span className="truncate">{workspaceBranch}</span>
        </span>
      )}
    </div>
  );
}

// --- Extracted ArchivedChatItem (module-level for stable identity + React.memo) ---

interface ArchivedChatItemProps {
  chat: ChatWithActivity;
  activeChatId: string | null;
  /** The chat's project, when no header above the row names it. */
  projectName?: string;
  /** The chat's workspace, when no header above the row names it. */
  workspaceName?: string;
}

const ArchivedChatItem = memo(function ArchivedChatItem({
  chat,
  activeChatId,
  projectName,
  workspaceName,
}: ArchivedChatItemProps) {
  const chatId = chat.id;
  const chatTitle = chat.title || "New chat";
  const isActive = activeChatId === chatId;
  const relativeTime = getRelativeTime(chat.updatedAt || chat.createdAt);
  const deleteChatMutation = useDeleteChat();
  const unarchiveMutation = useUnarchiveChat();

  const handleViewChat = () => {
    // The chat may be in another project: openChat moves the context there.
    openChat(chat).catch((error) => void toast.error(error));
  };

  const handleRestore = () => {
    unarchiveMutation.mutate(chatId);
  };

  const handleDelete = () => {
    deleteChatMutation.mutate(chatId);
  };

  return (
    <div
      data-chat-id={chatId}
      className={cn(
        "group relative flex w-full cursor-pointer items-center gap-2 overflow-hidden rounded-md border-l-2 border-transparent px-2.5 py-1.5 text-left font-sans text-xs transition-all duration-150",
        isActive
          ? "border-primary bg-primary/10 text-foreground font-semibold"
          : "bg-transparent text-foreground/80 hover:bg-muted/50 hover:text-foreground",
        "active:scale-[0.99]"
      )}
      onClick={handleViewChat}
    >
      <div className="flex-1 min-w-0 transition-all duration-200">
        <div className="truncate group-hover:text-foreground transition-colors duration-200">
          {chatTitle}
        </div>
        {(projectName || workspaceName) && (
          <ChatWhere chatId={chatId} projectName={projectName} workspaceBranch={workspaceName} />
        )}
      </div>

      <div className="flex-shrink-0 opacity-0 group-hover:opacity-100 transition-opacity duration-200">
        <Tooltip content="Restore chat" side="left" delay={300}>
          <button
            onClick={(e) => {
              e.stopPropagation();
              handleRestore();
            }}
            className="hover:bg-muted/70 rounded p-1 flex items-center justify-center"
          >
            <RotateCcw className="w-3 h-3 text-muted-foreground hover:text-foreground transition-colors duration-200" />
          </button>
        </Tooltip>
      </div>

      <div className="flex-shrink-0 relative flex items-center justify-center min-w-[2rem]">
        <span className="text-xs text-muted-foreground group-hover:opacity-0 transition-opacity duration-200">
          {relativeTime}
        </span>
        <Tooltip content="Delete chat" side="left" delay={300}>
          <button
            onClick={(e) => {
              e.stopPropagation();
              handleDelete();
            }}
            className="opacity-0 group-hover:opacity-100 transition-all duration-200 hover:bg-muted/70 rounded p-0.5 absolute inset-0 flex items-center justify-center"
          >
            <Trash2 className="w-3 h-3 text-destructive hover:text-destructive/80 transition-colors duration-200" />
          </button>
        </Tooltip>
      </div>
    </div>
  );
});

// --- WorktreeGroupComponent ---

interface WorktreeGroupComponentProps {
  id: string;
  title: string;
  subtitle?: string | ReactNode;
  icon: ReactNode;
  chats: ChatWithActivity[];
  isExpanded: boolean;
  onToggle: () => void;
  chatCount: number;
  renderChat: (chat: ChatWithActivity) => ReactNode;
  hasActiveChat?: boolean;
  onNewChat?: () => void;
  onArchiveWorktree?: () => void;
  onContextMenu?: (e: React.MouseEvent) => void;
  emptyState?: {
    message: string;
    onClick?: () => void;
  };
  /**
   * Inside a project section (All projects): the workspace is the level
   * under the project, so its header is a quiet branch label, not a second
   * heading that outweighs the project's.
   */
  nested?: boolean;
  /**
   * No header at all: the project's main workspace when it is the project's
   * only one, whose chats then sit directly under the project.
   */
  hideHeader?: boolean;
}

const WorktreeGroupComponent = memo(function WorktreeGroupComponent({
  id: _id,
  title,
  subtitle,
  icon,
  chats,
  isExpanded,
  onToggle,
  chatCount,
  renderChat,
  hasActiveChat,
  onNewChat,
  onArchiveWorktree,
  onContextMenu,
  emptyState,
  nested = false,
  hideHeader = false,
}: WorktreeGroupComponentProps) {
  if (hideHeader) {
    return (
      <div className="space-y-0.5" data-testid="sidebar-workspace-chats">
        {chats.map((chat) => (
          <div key={chat.id}>{renderChat(chat)}</div>
        ))}
      </div>
    );
  }
  return (
    <div className="mb-1">
      {/* Worktree Container */}
      <div
        className={cn(
          "overflow-hidden rounded-md transition-all duration-150",
          nested
            ? "bg-transparent"
            : hasActiveChat
              ? "bg-primary/10"
              : "bg-transparent hover:bg-muted/30"
        )}
      >
        {/* Header */}
        <div
          className={cn("group/header flex w-full items-center gap-1", nested ? "h-7" : "h-8")}
          onContextMenu={(e) => onContextMenu?.(e)}
        >
          <button
            onClick={onToggle}
            className="flex min-w-0 flex-1 items-center gap-2 rounded-md px-2 text-left transition-all duration-150"
          >
            <span className="flex h-4 w-4 shrink-0 items-center justify-center">
              {icon}
            </span>

            <div className="flex min-w-0 flex-1 items-center gap-1.5 text-left">
              <div
                className={cn(
                  "min-w-0 flex-1 truncate text-xs",
                  nested ? "font-medium" : "font-semibold uppercase tracking-wide",
                  hasActiveChat ? "text-primary" : "text-muted-foreground"
                )}
              >
                {title}
              </div>
              {subtitle && (
                <div className="flex-shrink-0 text-xs leading-none">
                  {typeof subtitle === "string" ? (
                    <span className="truncate text-muted-foreground">
                      {subtitle}
                    </span>
                  ) : (
                    subtitle
                  )}
                </div>
              )}
            </div>
          </button>

          {chatCount > 0 && (
            <span
              className={cn(
                "rounded-full px-1.5 py-0.5 text-2xs font-medium",
                hasActiveChat ? "bg-primary/10 text-primary" : "bg-muted text-muted-foreground"
              )}
            >
              {chatCount}
            </span>
          )}

          {/* New Chat Button - shows on hover */}
          {onNewChat && (
            <Tooltip
              content="New chat in this workspace"
              side="left"
              delay={300}
            >
              <button
                onClick={(e) => {
                  e.stopPropagation();
                  onNewChat();
                }}
                className="flex-shrink-0 rounded p-1 opacity-0 transition-opacity duration-150 hover:bg-muted/70 group-hover/header:opacity-100"
              >
                <Plus className="w-3.5 h-3.5 text-muted-foreground hover:text-foreground transition-colors duration-200" />
              </button>
            </Tooltip>
          )}

          {/* Archive Workspace Button - shows on hover */}
          {onArchiveWorktree && (
            <Tooltip
              content="Archive this workspace"
              side="left"
              delay={300}
            >
              <button
                onClick={(e) => {
                  e.stopPropagation();
                  onArchiveWorktree();
                }}
                className="flex-shrink-0 rounded p-1 opacity-0 transition-opacity duration-150 hover:bg-muted/70 group-hover/header:opacity-100"
              >
                <Archive className="w-3.5 h-3.5 text-muted-foreground hover:text-foreground transition-colors duration-200" />
              </button>
            </Tooltip>
          )}

          <button
            onClick={onToggle}
            className="flex flex-shrink-0 items-center rounded p-1 transition-all duration-150 hover:bg-muted/60"
          >
            {isExpanded ? (
              <ChevronDown className="w-3.5 h-3.5 text-foreground/60 transition-transform" />
            ) : (
              <ChevronRight className="w-3.5 h-3.5 text-foreground/60 transition-transform" />
            )}
          </button>
        </div>

        {/* Chats List */}
        {isExpanded && chats.length > 0 && (
          <div className={cn("space-y-0.5 pb-1 pt-0.5", nested ? "pl-4" : "pl-6")}>
            {chats.map((chat) => (
              <div key={chat.id}>{renderChat(chat)}</div>
            ))}
          </div>
        )}

        {/* Empty state for workspace with no chats */}
        {isExpanded && chats.length === 0 && emptyState && (
          <button
            onClick={(e) => {
              e.stopPropagation();
              emptyState.onClick?.();
            }}
            className={cn(
              "mb-1 ml-6 rounded-md px-2 py-1.5 text-left text-xs text-muted-foreground transition-colors",
              emptyState.onClick ? "hover:bg-muted/50 hover:text-foreground cursor-pointer" : "cursor-default"
            )}
          >
            {emptyState.message}
          </button>
        )}
      </div>
    </div>
  );
});

function SidebarComponent({
  paddingClass = "",
  onOpenWorkflows,
  onOpenRuns,
  onOpenInbox,
  onOpenChatSearch,
  onNavigateToSettings,
  onManageProjects,
  onOpenForge,
}: SidebarProps) {
  // The project filter (null: All projects) decides what the list shows; the
  // context project (the open chat's) does not, so opening a chat elsewhere
  // never re-scopes or unmounts the list.
  const projectFilterId = useWorkspaceStateStore((state) => state.projectFilterId);
  const setProjectFilter = useProjectStore((state) => state.setProjectFilter);
  const { projects } = useProjects();
  const { data: chatList } = useChatListWithWorkspaces(projectFilterId);
  const newChatShortcut = useShortcutLabel("newChat");
  const listedChats = useMemo(() => chatList?.chats ?? [], [chatList?.chats]);
  // The list carries each chat's workspace and every listed project's main
  // one, so the sidebar groups from this response alone.
  const mainWorktrees = chatList?.mainWorktrees;
  const scopedProjects = useMemo(
    () => (projectFilterId ? projects.filter((p) => p.id === projectFilterId) : projects),
    [projects, projectFilterId],
  );
  const projectNames = useMemo(() => new Map(projects.map((p) => [p.id, p.name])), [projects]);
  const projectNameOf = useCallback((projectId: string) => projectNames.get(projectId), [projectNames]);
  // Get active chat from navigation store
  const activeChatId = useChatStore((state) => state.activeChatId);
  // The open chat stays in the list even when the server left it out (§6.2
  // rule 4, the L2 fix). Read from the detail cache, which selectChat seeds
  // and the update stream keeps current, so a run opened from Runs or the
  // Inbox has a row, and one whose question was just answered keeps it.
  const { data: openedChat } = useChat(activeChatId ?? undefined);
  const releasedChatId = useSidebarPinStore((state) => state.releasedChatId);
  const forgetReleasedUnless = useSidebarPinStore((state) => state.forgetUnless);
  useEffect(() => {
    forgetReleasedUnless(activeChatId);
  }, [activeChatId, forgetReleasedUnless]);
  const chats = useMemo(
    () =>
      withOpenChatPinned(listedChats, openedChat?.id === activeChatId ? openedChat : undefined, {
        scope: projectFilterId,
        releasedChatId,
      }),
    [listedChats, openedChat, activeChatId, projectFilterId, releasedChatId],
  );
  // The rows the list renders: a run counted here has its own row, so the
  // automation pill does not count it again.
  const listedChatIds = useMemo(() => new Set(chats.map((chat) => chat.id)), [chats]);
  // ONE condition: the experimental gate. While the forge UI is unreleased this
  // is off in a packaged build, so the entry does not exist for anyone who has
  // not opted in.
  //
  // IT IS DELIBERATELY NOT ALSO GATED ON THE PROJECT BEING A FORGE PROJECT,
  // and that is a reversal. Apps and Observability are ORG-level surfaces
  // (every app the org runs; telemetry across all of them), not views of the
  // current project, so the entries sit beside New chat / Projects / Workflows
  // and stay PUT — a nav item that appears and disappears as you switch
  // projects is harder to learn than one that is always there, and which
  // project is current has no bearing on where they lead.
  //
  // It also removes a failure mode that actually bit us: the old gate required
  // a LIVE GetTopology call to succeed, so when that RPC broke (a report-type
  // mismatch, fixed separately) the whole feature silently vanished with
  // nothing in the UI to say why. A nav entry whose visibility depends on an
  // RPC is a nav entry that can be deleted by a bug.
  const showForgeEntry = isForgeUIEnabled();
  // Focus target for the "focus the chat list" shortcut. The list has no
  // single natural control to focus, so the pane container takes it and
  // ordinary tab-order takes over from there.
  const sidebarRootRef = useRef<HTMLDivElement>(null);
  useEffect(() => {
    const handleFocus = () => sidebarRootRef.current?.focus();
    window.addEventListener("focus-left-sidebar", handleFocus);
    return () => window.removeEventListener("focus-left-sidebar", handleFocus);
  }, []);

  // Activity from activityStore (SINGLE SOURCE OF TRUTH)
  const deleteChatMutation = useDeleteChat();
  const renameChatMutation = useRenameChat();
  const markUnreadMutation = useMarkUnread();
  const unarchiveMutation = useUnarchiveChat();
  const activities = useActivityStore((state) => state.activities);
  const workingThreads = useActivityStore((state) => state.workingThreads);
  // Archived chats from React Query (auto-fetches)
  const { data: archivedChats = [] } = useArchivedChats();
  
  const adoptRunMutation = useAdoptRun();
  const unadoptRunMutation = useUnadoptRun();

  // Chat list preferences
  const sortOrder = useChatListPreferencesStore((state) => state.sortOrder);
  const viewMode = useChatListPreferencesStore((state) => state.viewMode);
  const setSortOrder = useChatListPreferencesStore((state) => state.setSortOrder);
  const setViewMode = useChatListPreferencesStore((state) => state.setViewMode);

  // UI State
  const [chatListTab, setChatListTab] = useState<"active" | "archived">("active");

  const [expandedWorktreeGroups, setExpandedWorktreeGroups] = useState<
    Record<string, boolean>
  >({});
  const [isNoWorktreeExpanded, setIsNoWorktreeExpanded] = useState(true);
  // Project sections of the All projects view. Unset: open while the project
  // has chats, closed while it has none.
  const [expandedProjects, setExpandedProjects] = useState<Record<string, boolean>>({});
  const [expandedArchivedGroups, setExpandedArchivedGroups] = useState<
    Record<string, boolean>
  >({});
  const [contextMenu, setContextMenu] = useState<
    | ({ type: "chat"; chatId: string } & ContextMenuOpening)
    | ({ type: "worktree"; worktreeId: string } & ContextMenuOpening)
    | null
  >(null);
  const [editingChatId, setEditingChatId] = useState<string | null>(null);
  const [editingTitle, setEditingTitle] = useState("");

  // Ref for scroll container
  const scrollContainerRef = useRef<HTMLDivElement>(null);

  // Archived chats in the filter's scope
  const projectArchivedChats = useMemo(() => {
    if (!projectFilterId) return archivedChats;
    return archivedChats.filter((c) => c.projectId === projectFilterId);
  }, [archivedChats, projectFilterId]);

  // Activity detection - reads directly from activityStore (SINGLE SOURCE OF TRUTH)
  const getChatActivity = useCallback(
    (chat: Chat): ChatActivity => activities.get(chat.id) ?? ChatActivity.IDLE,
    [activities]
  );

  // Enhanced chat data with activity states
  // Don't debounce here - we want immediate updates for activity states
  const chatsWithActivity = useMemo((): ChatWithActivity[] => {
    // Filter out any archived chats from the active list
    // Also deduplicate by chatId to handle race conditions during chat creation
    const seenIds = new Set<string>();
    const activeChats = chats.filter((chat) => {
      if (chat.state === ChatState.ARCHIVED) return false;
      if (seenIds.has(chat.id)) {
        return false;
      }
      seenIds.add(chat.id);
      return true;
    });

    return activeChats.map((chat) => {
      const liveActivity = getChatActivity(chat);
      const activityState = activityToDotState(liveActivity);

      // Priority calculation for sorting (only truly active chats float to top)
      let priority = 0;
      if (activityState === "awaiting_input") priority += 1000; // Highest - needs user action
      if (activityState === "thinking") priority += 800; // AI actively working
      if (activityState === "error") priority += 400; // Errors need attention
      // Unread chats get a boost (but lower than active work)
      if (chat.unread) priority += 200;

      return {
        ...chat,
        activityState,
        liveActivity,
        liveWorkingThreads: workingThreads.get(chat.id) ?? 0,
        priority,
        lastActivity: chat.updatedAt,
      };
    });
  }, [chats, getChatActivity, workingThreads]);

  // Every workspace the list names, by id: the group headers' context menus
  // read a path from here.
  const workspacesById = useMemo(() => {
    const byId = new Map<string, WorktreeSummary>();
    for (const main of mainWorktrees ?? []) byId.set(main.id, main);
    for (const chat of listedChats) {
      if (chat.worktree) byId.set(chat.worktree.id, chat.worktree);
    }
    return byId;
  }, [listedChats, mainWorktrees]);

  // The workspace a chat is grouped under: its own summary, or its project's
  // main workspace for a chat with no worktree. Undefined only when the server
  // could not find the chat's worktree row.
  const getWorktreeForChat = useCallback(
    (chat: Chat) => workspaceOfChat(chat, mainWorktrees),
    [mainWorktrees]
  );

  // Filtered and sorted chats grouped by project → workspace (or flat list)
  const { projectGroups, activeGroups, flatList, archivedChats: filteredArchivedChats, archivedGroups } = useMemo(() => {
    // ONLY active (non-archived) chats from the main list; a chat on a closed
    // workspace is not listed as active (chatsWithActivity already dropped the
    // archived ones).
    const activeWorktreeChats = activeWorkspaceChats(chatsWithActivity, mainWorktrees);

    // Convert archived chats from store to ChatWithActivity format
    const archivedList: ChatWithActivity[] = projectArchivedChats.map((chat) => ({
      ...chat,
      activityState: "idle" as const,
      liveActivity: ChatActivity.IDLE,
      liveWorkingThreads: 0,
      priority: 0,
      lastActivity: chat.updatedAt,
    }));

    // Sort chats based on user preference. The comparator is shared with the
    // navigation store so keyboard next/prev walks the list in the order it is
    // rendered in.
    const sortChatList = (chats: ChatWithActivity[]) =>
      sortChats(chats, sortOrder, chatNeedsAttention);
    const groupOptions = { sortOrder, needsAttention: chatNeedsAttention };

    // All projects: project sections, each grouped by workspace. One project:
    // its workspace groups directly.
    const projectGroups = projectFilterId
      ? []
      : groupByProject(activeWorktreeChats, mainWorktrees, scopedProjects, groupOptions);
    const groupsArray: ChatGroup[] = projectFilterId
      ? groupByWorkspace(projectFilterId, activeWorktreeChats, mainWorktrees, groupOptions)
      : projectGroups.flatMap((group) => group.workspaces);

    // Create flat list for flat view mode
    const flatList = sortChatList(activeWorktreeChats);

    // Group archived chats by project across projects, as the active list
    // does (two projects' "main" workspaces used to merge into one group
    // that named neither), and by workspace within one project.
    const archivedGroupsMap = new Map<string, ArchivedChatGroup>();

    archivedList.forEach((chat) => {
      if (!projectFilterId) {
        const groupKey = `project:${chat.projectId}`;
        if (!archivedGroupsMap.has(groupKey)) {
          archivedGroupsMap.set(groupKey, {
            worktreeName: projectNames.get(chat.projectId) ?? "Project",
            chats: [],
            isWorktreeArchived: false,
            projectId: chat.projectId,
          });
        }
        archivedGroupsMap.get(groupKey)!.chats.push(chat);
        return;
      }
      // Use worktree_name from archived chat metadata, or worktree_id, or fallback to "Unknown Workspace"
      const groupKey = chat.worktreeName || chat.worktreeId || "unknown";
      const displayName = chat.worktreeName || "Unknown Workspace";

      if (!archivedGroupsMap.has(groupKey)) {
        archivedGroupsMap.set(groupKey, {
          worktreeName: displayName,
          worktreeId: chat.worktreeId,
          chats: [],
          isWorktreeArchived: !!chat.worktreeDeletedAt,
        });
      }

      archivedGroupsMap.get(groupKey)!.chats.push(chat);
    });

    // Sort chats within each archived group
    archivedGroupsMap.forEach((group) => {
      group.chats = sortChatList([...group.chats]);
    });

    // Convert to array and sort groups by their leading chat
    const archivedGroupsArray = Array.from(archivedGroupsMap.values());
    archivedGroupsArray.sort((a, b) =>
      compareChatGroups(a.chats, b.chats, sortOrder, chatNeedsAttention)
    );

    return {
      projectGroups,
      activeGroups: groupsArray,
      flatList,
      archivedChats: sortChatList([...archivedList]),
      archivedGroups: archivedGroupsArray,
    };
  }, [
    chatsWithActivity,
    projectArchivedChats,
    sortOrder,
    mainWorktrees,
    projectFilterId,
    scopedProjects,
    projectNames,
  ]);

  // The global New chat: the filter's project, else the open chat's, else the
  // last one used (openNewChat).
  const handleNewChat = () => {
    void openNewChat();
  };

  const handleSwitchToWorktreeNewChat = useCallback(
    async (worktreeId: string, projectId: string) => {
      // Show New Chat page with the target workspace (and its project) selected
      await openNewChat({ projectId, worktreeId });
    },
    []
  );

  const handleChatClick = useCallback(async (chat: Chat) => {
    // openChat switches the context project and workspace to the chat's (and
    // saves/restores viewer state), then selects it.
    try {
      await openChat(chat);
    } catch (error) {
      void toast.error(error);
    }
  }, []);

  const handleFilterChange = useCallback(
    (projectId: string | null) => {
      void setProjectFilter(projectId);
    },
    [setProjectFilter],
  );

  // Chats waiting on the user, per project: the switcher shows them so a
  // filtered list never hides that another project needs you.
  const needsYouByProject = useMemo(() => {
    const counts = new Map<string, number>();
    for (const chat of chatsWithActivity) {
      if (!chatNeedsAttention(chat)) continue;
      counts.set(chat.projectId, (counts.get(chat.projectId) ?? 0) + 1);
    }
    return counts;
  }, [chatsWithActivity]);

  const handleChatContextMenu = useCallback((e: React.MouseEvent, chat: Chat) => {
    e.preventDefault();
    e.stopPropagation();
    setContextMenu({ ...contextMenuOpening(e), type: "chat", chatId: chat.id });
  }, []);

  const handleWorktreeContextMenu = useCallback((e: React.MouseEvent, worktreeId: string) => {
    e.preventDefault();
    e.stopPropagation();

    if (!workspacesById.get(worktreeId)?.path) return;

    setContextMenu({ ...contextMenuOpening(e), type: "worktree", worktreeId });
  }, [workspacesById]);

  const copyToClipboard = (text: string) => {
    navigator.clipboard.writeText(text).catch((err) => {
      console.error("Failed to copy to clipboard:", err);
    });
  };

  const handleRenameChat = useCallback(
    (chatId: string) => {
      const chat = chats.find((c) => c.id === chatId);
      if (!chat) {
        console.error("[Rename] Chat not found:", chatId);
        return;
      }

      const currentTitle = chat.title || "New chat";

      // Update state immediately - React will batch these updates
      setEditingChatId(chatId);
      setEditingTitle(currentTitle);
    },
    [chats]
  );

  const handleSaveRename = useCallback(async (chatId: string) => {
    const chat = chats.find((c) => c.id === chatId);
    if (!chat) return;

    const currentTitle = chat.title || "New chat";
    const newTitle = editingTitle.trim();

    if (newTitle && newTitle !== currentTitle) {
      try {
        await renameChatMutation.mutateAsync({ chatId, title: newTitle });
      } catch (error) {
        console.error("Failed to rename chat:", error);
        alert("Failed to rename chat. Please try again.");
      }
    }

    setEditingChatId(null);
    setEditingTitle("");
  }, [chats, editingTitle, renameChatMutation]);

  const handleCancelRename = useCallback(() => {
    setEditingChatId(null);
    setEditingTitle("");
  }, []);

  const getChatContextMenuItems = (chatId: string): MenuGroup[] => {
    const chat = chats.find((c) => c.id === chatId);
    if (!chat) return [];

    const menuItems: MenuGroup["items"] = [
      {
        label: "Rename",
        icon: <Edit className="w-4 h-4" />,
        onClick: () => {
          handleRenameChat(chatId);
        },
      },
      {
        label: "Copy Chat ID",
        icon: <Copy className="w-4 h-4" />,
        onClick: () => copyToClipboard(chatId),
      },
    ];

    const chatWorktree = getWorktreeForChat(chat);
    if (chatWorktree?.path) {
      menuItems.push({
        label: "Copy Workspace Path",
        icon: <Copy className="w-4 h-4" />,
        onClick: () => copyToClipboard(chatWorktree.path),
      });
    }

    // Add "Mark Unread" option if chat is not already unread
    if (!chat.unread) {
      menuItems.push({
        label: "Mark Unread",
        icon: <Mail className="w-4 h-4" />,
        onClick: () => markUnreadMutation.mutate(chatId),
      });
    }

    // Un-adopt (§6.3): the run leaves the chat list and lives in Runs again.
    // Not archive: nothing about the run changes but where it is listed.
    if (isAdoptedAutomation(chat)) {
      menuItems.push({
        label: "Move back to Runs",
        icon: <Activity className="w-4 h-4" />,
        onClick: () => handleUnadoptChat(chatId),
      });
    }

    return [
      { items: menuItems },
      {
        items: [
          {
            label: "Delete",
            icon: <Trash2 className="w-4 h-4" />,
            onClick: () => deleteChatMutation.mutate(chatId),
            variant: "danger",
          },
        ],
      },
    ];
  };

  const getWorktreeContextMenuItems = (worktreeId: string): MenuGroup[] => {
    const worktree = workspacesById.get(worktreeId);
    if (!worktree?.path) return [];

    return [
      {
        items: [
          {
            label: "Copy Workspace Path",
            icon: <Copy className="w-4 h-4" />,
            onClick: () => copyToClipboard(worktree.path),
          },
        ],
      },
    ];
  };

  const handleUnadoptChat = useCallback(
    (chatId: string) => {
      // Released first: the chat may be the open one, and pinning it would
      // keep the row the user just asked to move.
      useSidebarPinStore.getState().release(chatId);
      unadoptRunMutation.mutate({ chatId }, {
        onSuccess: () => {
          toast.notify("Moved back to Runs", {
            action: {
              label: "Undo",
              onClick: () => adoptRunMutation.mutate({ chatId }),
            },
          });
        },
        onError: (error) => {
          useSidebarPinStore.getState().forgetUnless(null);
          void toast.error(error);
        },
      });
    },
    [adoptRunMutation, unadoptRunMutation],
  );

  const handleArchiveChat = useCallback(async (chatId: string) => {
    await deleteChatMutation.mutateAsync(chatId);
    toast.notify("Chat archived", {
      action: {
        label: "Undo",
        onClick: () => {
          unarchiveMutation.mutate(chatId);
        },
      },
    });
  }, [deleteChatMutation, unarchiveMutation]);

  // Reveal the active chat (switch tab, expand its group, scroll it into view)
  // when the selection changes — e.g. via keyboard navigation.
  //
  // This runs at most once per (chat, view mode): revealing is a response to the
  // selection changing, not a state the sidebar continuously enforces. Re-running
  // it on tab/expansion state would undo the user's own clicks, and re-running it
  // on every chat-list refetch would yank the sidebar around mid-browse.
  const revealedChatKeyRef = useRef<string | null>(null);
  useEffect(() => {
    if (!activeChatId || !scrollContainerRef.current) return;

    // Find the chat in the active list first
    let chat = chatsWithActivity.find((c) => c.id === activeChatId);
    let isArchived = false;

    // If not found in active chats, check archived chats
    if (!chat) {
      const archivedChat = projectArchivedChats.find(
        (c) => c.id === activeChatId
      );
      if (archivedChat) {
        chat = {
          ...archivedChat,
        } as ChatWithActivity;
        isArchived = true;
      }
    }

    // The chat lists load asynchronously, so an unknown id here just means the
    // data hasn't arrived yet. Leave the key unclaimed and retry on the next
    // list update.
    if (!chat) return;

    const revealKey = `${activeChatId}:${viewMode}`;
    if (revealedChatKeyRef.current === revealKey) return;
    revealedChatKeyRef.current = revealKey;

    setChatListTab(isArchived ? "archived" : "active");

    // If in grouped view and not archived, expand the project section and the
    // worktree group containing this chat
    if (!isArchived && viewMode === "grouped") {
      if (chat.projectId && expandedProjects[chat.projectId] === false) {
        setExpandedProjects((prev) => ({ ...prev, [chat.projectId]: true }));
      }
      const worktree = getWorktreeForChat(chat);
      if (worktree) {
        const worktreeId = worktree.id;
        const isNoWorktree = worktreeId.startsWith("no-worktree") || !worktreeId;
        
        if (isNoWorktree) {
          if (!isNoWorktreeExpanded) {
            setIsNoWorktreeExpanded(true);
          }
        } else {
          if (!expandedWorktreeGroups[worktreeId]) {
            setExpandedWorktreeGroups((prev) => ({
              ...prev,
              [worktreeId]: true,
            }));
          }
        }
      }
    }

    // If archived and in grouped view, expand the archived group containing this chat
    if (isArchived && viewMode === "grouped") {
      const containingGroup = archivedGroups.find((group) =>
        group.chats.some((c) => {
          return c.id === activeChatId;
        })
      );
      if (containingGroup) {
        const groupKey = containingGroup.worktreeId || containingGroup.worktreeName;
        if (!expandedArchivedGroups[groupKey]) {
          setExpandedArchivedGroups((prev) => ({
            ...prev,
            [groupKey]: true,
          }));
        }
      }
    }

    // Scroll to the active chat item after a brief delay to allow DOM updates
    // (especially for expanding groups)
    const timeoutId = setTimeout(() => {
      const chatElement = scrollContainerRef.current?.querySelector(
        `[data-chat-id="${CSS.escape(activeChatId)}"]`
      );
      if (chatElement) {
        chatElement.scrollIntoView({
          block: "nearest",
          behavior: "auto",
        });
      }
    }, 100);

    return () => clearTimeout(timeoutId);
    // The expansion setters and getWorktreeForChat are intentionally omitted:
    // reading them via the render closure keeps this effect from re-running when
    // the user toggles a group open or closed.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [activeChatId, viewMode, chatsWithActivity, projectArchivedChats, archivedGroups]);


  const toggleWorktreeGroup = (worktreeId: string) => {
    setExpandedWorktreeGroups((prev) => ({
      ...prev,
      [worktreeId]: !prev[worktreeId],
    }));
  };


  // Memoized icon to avoid new JSX reference each render
  const worktreeIcon = useMemo(
    () => <FolderGit2 className="w-3.5 h-3.5 text-primary/80 flex-shrink-0" />,
    []
  );
  const nestedWorktreeIcon = useMemo(
    () => <GitBranch className="w-3 h-3 text-muted-foreground flex-shrink-0" aria-hidden="true" />,
    []
  );

  // `layout`: a workspace inside a project section is nested under it, and
  // the project's only workspace, its main one, has no header of its own.
  const renderWorktreeGroup = useCallback(
    (group: ChatGroup, layout: { nested?: boolean; hideHeader?: boolean } = {}) => {
      // Handle main branch (no worktree) special case
      const isNoWorktree = group.worktreeId.startsWith("no-worktree");
      const isExpanded = isNoWorktree
        ? isNoWorktreeExpanded
        : expandedWorktreeGroups[group.worktreeId] ?? true;

      const handleToggle = () => {
        if (isNoWorktree) {
          setIsNoWorktreeExpanded(!isNoWorktreeExpanded);
        } else {
          toggleWorktreeGroup(group.worktreeId);
        }
      };

      // Check if this group contains the active chat
      const hasActiveChat = group.chats.some(
        (chat) => chat.id === activeChatId
      );

      // Handlers for worktree actions
      const handleNewChat = isNoWorktree
        ? undefined
        : () => {
            void handleSwitchToWorktreeNewChat(group.worktreeId, group.projectId);
          };

      const handleArchiveWorktree = (isNoWorktree || group.isMain)
        ? undefined
        : async () => {
            
            await archiveWorktree(group.worktreeId);
          };

      const handleWorktreeMenu = isNoWorktree
        ? undefined
        : (e: React.MouseEvent) => handleWorktreeContextMenu(e, group.worktreeId);

      const emptyState = group.chats.length === 0
        ? {
            message: "No chats",
            onClick: group.isMain
              ? () => {
                  void handleSwitchToWorktreeNewChat(group.worktreeId, group.projectId);
                }
              : undefined,
          }
        : undefined;

      return (
        <WorktreeGroupComponent
          key={group.worktreeId}
          id={group.worktreeId}
          title={group.worktreeBranch}
          subtitle={null}
          icon={layout.nested ? nestedWorktreeIcon : worktreeIcon}
          nested={layout.nested}
          hideHeader={layout.hideHeader}
          chats={group.chats}
          isExpanded={isExpanded}
          onToggle={handleToggle}
          chatCount={group.chats.length}
          renderChat={(chat) => (
            <ChatItem
              chat={chat}
              inGroup={true}
              editingChatId={editingChatId}
              editingTitle={editingTitle}
              activeChatId={activeChatId}
              onChatClick={handleChatClick}
              onContextMenu={handleChatContextMenu}
              onEditingTitleChange={setEditingTitle}
              onSaveRename={handleSaveRename}
              onCancelRename={handleCancelRename}
              onArchiveChat={handleArchiveChat}
            />
          )}
          hasActiveChat={hasActiveChat}
          onNewChat={handleNewChat}
          onArchiveWorktree={handleArchiveWorktree}
          onContextMenu={handleWorktreeMenu}
          emptyState={emptyState}
        />
      );
    },
    [
      expandedWorktreeGroups,
      isNoWorktreeExpanded,
      activeChatId,
      editingChatId,
      editingTitle,
      worktreeIcon,
      nestedWorktreeIcon,
      handleChatClick,
      handleChatContextMenu,
      handleSaveRename,
      handleArchiveChat,
      handleSwitchToWorktreeNewChat,
      handleCancelRename,
      handleWorktreeContextMenu,
    ]
  );

  // A project section of the All projects view: the project's name, its chat
  // count, New chat in this project, and its workspace groups.
  //
  // The project is the strongest level of the list, so which project a chat
  // is in reads at a glance: a project mark and the name at body size, a
  // header that stays pinned while its chats scroll under it, and a guide
  // line down the chats it holds. Its workspaces are quiet branch labels
  // under it, and a project whose only workspace is main lists its chats
  // directly: a "main" header under every project said nothing.
  const renderProjectSection = (group: (typeof projectGroups)[number]) => {
    const isExpanded = expandedProjects[group.projectId] ?? group.chats.length > 0;
    const hasActiveChat = group.chats.some((chat) => chat.id === activeChatId);
    const toggle = () =>
      setExpandedProjects((prev) => ({ ...prev, [group.projectId]: !isExpanded }));
    const onlyMain = group.workspaces.length === 1 && group.workspaces[0]?.isMain === true;
    const mainWorkspace = onlyMain ? group.workspaces[0] : undefined;
    return (
      <section
        key={group.projectId}
        aria-label={group.projectName}
        data-testid={`sidebar-project-${group.projectId}`}
      >
        <div
          className="group/project sticky top-0 z-10 -mx-1 flex h-8 items-center gap-1 bg-card px-1"
          data-testid={`sidebar-project-header-${group.projectId}`}
          // The main workspace's header is folded into the project's, so its
          // actions are here.
          onContextMenu={
            mainWorkspace && !mainWorkspace.worktreeId.startsWith("no-worktree")
              ? (e) => handleWorktreeContextMenu(e, mainWorkspace.worktreeId)
              : undefined
          }
        >
          <button
            type="button"
            onClick={toggle}
            aria-expanded={isExpanded}
            className={cn(
              "flex min-w-0 flex-1 items-center gap-2 rounded-md px-1 text-left text-sm font-semibold transition-colors",
              "focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40",
              hasActiveChat ? "text-primary" : "text-foreground",
            )}
          >
            <ProjectMark name={group.projectName} active={hasActiveChat} />
            <span className="min-w-0 truncate">{group.projectName}</span>
            {isExpanded ? (
              <ChevronDown className="h-3.5 w-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
            ) : (
              <ChevronRight className="h-3.5 w-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
            )}
          </button>
          {group.chats.length > 0 && (
            <span className="rounded-full bg-muted px-1.5 py-0.5 text-2xs font-medium text-muted-foreground">
              {group.chats.length}
            </span>
          )}
          <Tooltip content="New chat in this project" side="left" delay={300}>
            <button
              type="button"
              aria-label={`New chat in ${group.projectName}`}
              onClick={() => void openNewChat({ projectId: group.projectId })}
              className="flex-shrink-0 rounded p-1 opacity-0 transition-opacity duration-150 hover:bg-muted/70 focus:opacity-100 group-hover/project:opacity-100"
            >
              <Plus className="h-3.5 w-3.5 text-muted-foreground" />
            </button>
          </Tooltip>
        </div>
        {isExpanded && (
          <div className="ml-3.5 space-y-0.5 border-l border-border/60 pb-1 pl-2 pt-0.5">
            {group.workspaces.map((workspace) =>
              renderWorktreeGroup(workspace, { nested: true, hideHeader: onlyMain }),
            )}
            {group.chats.length === 0 && (
              <button
                type="button"
                onClick={() => void openNewChat({ projectId: group.projectId })}
                className="rounded-md px-2 py-1.5 text-left text-xs text-muted-foreground transition-colors hover:bg-muted/50 hover:text-foreground"
              >
                No chats yet · start one
              </button>
            )}
          </div>
        )}
      </section>
    );
  };

  // Toggle archived workspace group expansion
  const toggleArchivedGroup = (groupKey: string) => {
    setExpandedArchivedGroups((prev) => ({
      ...prev,
      [groupKey]: !prev[groupKey],
    }));
  };

  // Render archived workspace group (similar to renderWorktreeGroup but with archived styling)
  const renderArchivedGroup = useCallback(
    (group: ArchivedChatGroup) => {
      const groupKey = group.worktreeId || group.worktreeName;
      const isExpanded = expandedArchivedGroups[groupKey] ?? false; // Collapsed by default

      const handleToggle = () => {
        toggleArchivedGroup(groupKey);
      };

      // Check if this group contains the active chat
      const hasActiveChat = group.chats.some(
        (chat) => chat.id === activeChatId
      );

      // Icon showing this workspace is archived
      const subtitle = group.isWorktreeArchived ? (
        <Tooltip content="Workspace archived" side="right" delay={300}>
          <Archive className="w-3 h-3 text-muted-foreground/60" />
        </Tooltip>
      ) : null;

      const handleWorktreeMenu = group.worktreeId
        ? (e: React.MouseEvent) => handleWorktreeContextMenu(e, group.worktreeId as string)
        : undefined;

      // A whole project's archive: its mark, and each row names its workspace.
      const isProject = Boolean(group.projectId);
      return (
        <WorktreeGroupComponent
          key={groupKey}
          id={groupKey}
          title={group.worktreeName}
          subtitle={subtitle}
          icon={
            isProject ? (
              <ProjectMark name={group.worktreeName} active={hasActiveChat} className="h-4 w-4" />
            ) : (
              <FolderGit2 className="w-3.5 h-3.5 text-muted-foreground/60 flex-shrink-0" />
            )
          }
          chats={group.chats}
          isExpanded={isExpanded}
          onToggle={handleToggle}
          chatCount={group.chats.length}
          renderChat={(chat) => (
            <ArchivedChatItem
              chat={chat}
              activeChatId={activeChatId}
              workspaceName={isProject ? chat.worktreeName || undefined : undefined}
            />
          )}
          hasActiveChat={hasActiveChat}
          onContextMenu={handleWorktreeMenu}
        />
      );
    },
    [expandedArchivedGroups, activeChatId, handleWorktreeContextMenu]
  );

  const activeChatCount = activeGroups.reduce((total, group) => total + group.chats.length, 0);
  const visibleChatCount = chatListTab === "active" ? activeChatCount : filteredArchivedChats.length;
  const hasAnyVisibleChats = visibleChatCount > 0;
  const emptyTitle = chatListTab === "active" ? "No active chats" : "No archived chats";
  const emptyDescription =
    chatListTab === "active" ? "Create your first chat to get started" : "Archived chats will appear here";
  const currentSortOption =
    SORT_OPTIONS.find((option) => option.value === sortOrder) ?? SORT_OPTIONS[0];
  if (!currentSortOption) {
    return null;
  }

  return (
    <div
      ref={sidebarRootRef}
      className="flex h-full flex-col bg-card dense-ui"
      data-onboarding="left-sidebar"
      // Marks the focus context for the keyboard dispatcher and gives the pane
      // something focusable, so "focus the chat list" has a target.
      data-context="left-sidebar"
      tabIndex={-1}
    >
      {/* Header section - only when not fullscreen */}
      {paddingClass && (
        <div className="h-12 border-b border-border/40 bg-card"></div>
      )}

      <div className="border-b border-border/50 bg-card px-3 pb-3 pt-2">
        <nav className="space-y-1" aria-label="Chat sidebar navigation">
          <SidebarNavButton
            icon={<Edit className="h-4 w-4" />}
            label="New chat"
            onClick={handleNewChat}
            testId="create-chat-button"
            shortcut={newChatShortcut}
          />
          {/* Apps (/apps) is the org's index of every app it runs, each one a
              drill-in to its console (environments, releases, observability) —
              the ONE entry into the running side — so it sits directly under
              New chat: after writing code, the next place to go is what that
              code runs as. */}
          {showForgeEntry && (
            <SidebarNavButton
              icon={<Boxes className="h-4 w-4" />}
              label="Apps"
              onClick={onOpenForge}
              testId="sidebar-forge-button"
              onboardingId="apps-button"
            />
          )}
          {/* What is live, one click from anywhere: each app's console and its site. */}
          {showForgeEntry && <SidebarApps />}
          <InboxNavItem onOpen={onOpenInbox} />
          {/* One entry for the whole area: Workflows and Runs are
              its tabs (WORKFLOW_UI.md §1.2), so they get no entries here. */}
          <SidebarNavButton
            icon={<Workflow className="h-4 w-4" />}
            label="Workflows"
            onClick={onOpenWorkflows}
            onboardingId="workflow-button"
          />
          <SidebarNavButton
            icon={<Search className="h-4 w-4" />}
            label="Search"
            onClick={onOpenChatSearch}
          />
        </nav>
      </div>

      <div className="bg-card px-3 pb-2 pt-3">
        <div className="mb-2 flex items-center gap-2">
          {/* The project is a filter on the list, never a gate in front of it. */}
          <div className="flex min-w-0 flex-1 items-center" data-onboarding="project-filter">
            <ProjectFilterMenu
              filterId={projectFilterId}
              projects={projects}
              onChange={handleFilterChange}
              onAddProject={() => openAddProject()}
              onManageProjects={() => onManageProjects?.()}
              needsYouByProject={needsYouByProject}
            />
          </div>

          <DropdownMenu
            align="right"
            density="compact"
            label="Sort chats"
            menuClassName="min-w-[190px]"
            trigger={(props) => (
              <Tooltip
                content={`Sort: ${currentSortOption.label}`}
                side="bottom"
                delay={300}
                triggerClassName={CHAT_HEADER_ACTION_TOOLTIP_CLASS}
              >
                <button
                  type="button"
                  {...props}
                  className={cn(
                    CHAT_HEADER_ACTION_BUTTON_CLASS,
                    "h-6 w-6 text-muted-foreground hover:bg-muted/60 hover:text-foreground"
                  )}
                  aria-label="Sort chats"
                >
                  {currentSortOption.icon}
                </button>
              </Tooltip>
            )}
            groups={[
              {
                label: "List",
                selection: "single",
                items: [
                  {
                    label: "Active chats",
                    icon: <MessageSquare className="h-3.5 w-3.5" />,
                    checked: chatListTab === "active",
                    onClick: () => setChatListTab("active"),
                  },
                  {
                    label: "Archived chats",
                    icon: <Archive className="h-3.5 w-3.5" />,
                    checked: chatListTab === "archived",
                    onClick: () => setChatListTab("archived"),
                  },
                ],
              },
              {
                label: "Sort by",
                selection: "single",
                items: SORT_OPTIONS.map((option) => ({
                  label: option.label,
                  icon: option.icon,
                  checked: sortOrder === option.value,
                  onClick: () => setSortOrder(option.value),
                })),
              },
            ]}
          />

          <DropdownMenu
            align="right"
            density="compact"
            label="View options"
            menuClassName="min-w-[220px]"
            trigger={(props) => (
              <Tooltip
                content="View options"
                side="bottom"
                delay={300}
                triggerClassName={CHAT_HEADER_ACTION_TOOLTIP_CLASS}
              >
                <button
                  type="button"
                  {...props}
                  className={cn(
                    CHAT_HEADER_ACTION_BUTTON_CLASS,
                    "h-6 w-6 text-muted-foreground hover:bg-muted/60 hover:text-foreground"
                  )}
                  aria-label="View options"
                >
                  {viewMode === "grouped" ? (
                    <LayoutGrid className={CHAT_HEADER_ACTION_ICON_CLASS} />
                  ) : (
                    <LayoutList className={CHAT_HEADER_ACTION_ICON_CLASS} />
                  )}
                </button>
              </Tooltip>
            )}
            groups={[
              {
                label: "View",
                selection: "single",
                items: [
                  {
                    label: projectFilterId ? "Grouped by workspace" : "Grouped by project",
                    icon: <LayoutGrid className="h-3.5 w-3.5" />,
                    checked: viewMode === "grouped",
                    onClick: () => setViewMode("grouped"),
                  },
                  {
                    label: "Flat list",
                    icon: <LayoutList className="h-3.5 w-3.5" />,
                    checked: viewMode === "flat",
                    onClick: () => setViewMode("flat"),
                  },
                ],
              },
            ]}
          />

        </div>
      </div>

      <div className="flex flex-1 flex-col overflow-hidden">
        <div ref={scrollContainerRef} className="min-h-0 flex-1 overflow-y-auto px-3 pb-3 pt-1">
          {chatListTab === "active" ? (
            viewMode === "grouped" ? (
              projectFilterId ? (
                activeGroups.length > 0 && (
                  <div className="space-y-1.5">
                    {activeGroups.map((group) => renderWorktreeGroup(group))}
                  </div>
                )
              ) : (
                projectGroups.length > 0 && (
                  <div className="space-y-2">
                    {projectGroups.map((group) => renderProjectSection(group))}
                  </div>
                )
              )
            ) : (
              flatList.length > 0 && (
                <div className="space-y-1">
                  {flatList.map((chat) => {
                    const worktree = getWorktreeForChat(chat);
                    // Across projects, the row names its project first; the
                    // branch only when it is not the project's main checkout.
                    const projectName = projectFilterId ? undefined : projectNameOf(chat.projectId);
                    const branch = projectFilterId || !worktree?.isMain ? worktree?.branch : undefined;
                    return (
                      <ChatItem
                        key={chat.id}
                        chat={chat}
                        inGroup={false}
                        editingChatId={editingChatId}
                        editingTitle={editingTitle}
                        projectName={projectName}
                        workspaceBranch={branch || undefined}
                        activeChatId={activeChatId}
                        onChatClick={handleChatClick}
                        onContextMenu={handleChatContextMenu}
                        onEditingTitleChange={setEditingTitle}
                        onSaveRename={handleSaveRename}
                        onCancelRename={handleCancelRename}
                        onArchiveChat={handleArchiveChat}
                      />
                    );
                  })}
                </div>
              )
            )
          ) : filteredArchivedChats.length > 0 ? (
            viewMode === "grouped" ? (
              <div className="space-y-2">
                {archivedGroups.map((group) => renderArchivedGroup(group))}
              </div>
            ) : (
              <div className="space-y-1">
                {filteredArchivedChats.map((chat) => (
                  <ArchivedChatItem
                    key={chat.id}
                    chat={chat}
                    activeChatId={activeChatId}
                    projectName={projectFilterId ? undefined : projectNameOf(chat.projectId)}
                  />
                ))}
              </div>
            )
          ) : null}

          {/* Empty State */}
          {!hasAnyVisibleChats && (
            <div className="px-2 py-8 text-center text-muted-foreground">
              <div className="mx-auto mb-3 flex h-10 w-10 items-center justify-center rounded-full border border-border/50 bg-muted/30">
                {chatListTab === "active" ? <Plus className="h-4 w-4" /> : <Archive className="h-4 w-4" />}
              </div>
              <p className="mb-1 text-sm font-medium text-foreground/75">{emptyTitle}</p>
              <p className="text-xs text-muted-foreground/70">{emptyDescription}</p>
              {chatListTab === "active" && (
                <div className="mt-3 flex flex-col items-center gap-2">
                  <Button
                    onClick={handleNewChat}
                    variant="ghost"
                    size="sm"
                  >
                    New chat
                  </Button>
                  {viewMode === "flat" && activeGroups.length > 0 && (
                    <Button
                      onClick={() => {
                        const mainGroup = activeGroups.find((g) => g.isMain);
                        if (mainGroup) {
                          void handleSwitchToWorktreeNewChat(mainGroup.worktreeId, mainGroup.projectId);
                        }
                      }}
                      variant="ghost"
                      size="sm"
                    >
                      Go to main workspace
                    </Button>
                  )}
                </div>
              )}
            </div>
          )}
        </div>
      </div>

      <div className="border-t border-border/40 bg-card px-3 py-2">
        <AutomationActivityPill listedChatIds={listedChatIds} onOpenRuns={onOpenRuns} />
        {/* Renders nothing for a person with one org. */}
        <OrgSwitcher />
        <SidebarNavButton
          icon={<Settings className="h-4 w-4" />}
          label="Settings"
          onClick={onNavigateToSettings}
        />
      </div>

      {/* One context menu for every row; its items follow the row. */}
      {contextMenu && (
        <ContextMenu
          label={contextMenu.type === "chat" ? "Chat actions" : "Workspace actions"}
          groups={
            contextMenu.type === "chat"
              ? getChatContextMenuItems(contextMenu.chatId)
              : getWorktreeContextMenuItems(contextMenu.worktreeId)
          }
          open
          position={{ x: contextMenu.x, y: contextMenu.y }}
          openVia={contextMenu.via}
          onOpenChange={(open) => {
            if (open) return;
            setContextMenu(null);
            // Back to the composer, unless the chosen item focused an input
            // of its own (an inline rename): after the item's state commits.
            requestAnimationFrame(() => {
              if (!isInputFocused()) focusChatInput();
            });
          }}
        />
      )}
      {/* Render complete */}
    </div>
  );
}

export const Sidebar = SidebarComponent;
