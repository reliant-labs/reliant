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
import { WorktreeStatus } from "../../gen/reliant/v1/worktree_pb";
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
  Check,
  FolderOpen,
  Search,
  Settings,
  Workflow,
} from "lucide-react";
import { useChatStore } from "../../store/chatStore";
import { useChatList, useArchivedChats, useDeleteChat, useRenameChat, useUnarchiveChat } from "../../hooks/chat-queries";
import { useMarkUnread } from "../../hooks/message-queries";
import { useChatNavigationStore } from "../../store/chatNavigationStore";
import { useWorktreeStore } from "../../store/worktreeStore";
import { useProcessStore } from "../../store/processStore";
import { useProjectStore } from "../../store/projectStore";
import { cn } from "../../lib/utils";
import { toast } from "../../lib/toast-manager";
import { Tooltip } from "../ui/Tooltip";
import { Button } from "../ui/Button";
import { ContextMenu } from "../ui/ContextMenu";
import type { ContextMenuItem } from "../ui/ContextMenu";
import type { Chat } from "../../api/client";
import {
  useChatListPreferencesStore,
  type ChatSortOption,
} from "../../store/chatListPreferencesStore";
import { Dropdown } from "../ui/Dropdown";
import { ActivityDot, type ChatActivityState } from "../ui/ActivityDot";
import { useActivityStore, activityToDotState, ChatActivity } from "../../store/activityStore";

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
  onNavigateToProjectPicker?: () => void;
  onOpenWorkflows?: () => void;
  onOpenChatSearch?: () => void;
  onNavigateToSettings?: () => void;
}

interface SidebarNavButtonProps {
  icon: ReactNode;
  label: string;
  onClick?: () => void;
  testId?: string;
}

function SidebarNavButton({ icon, label, onClick, testId }: SidebarNavButtonProps) {
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
    >
      <span className="flex h-4 w-4 shrink-0 items-center justify-center text-muted-foreground">
        {icon}
      </span>
      <span className="min-w-0 truncate">{label}</span>
    </button>
  );
}

// Worktree group structure
interface ChatGroup {
  worktreeId: string;
  worktreeName: string;
  worktreeBranch: string;
  chats: ChatWithActivity[];
  hasActivity: boolean;
  isMain: boolean;
}

// Archived worktree group structure
interface ArchivedChatGroup {
  worktreeName: string; // Display name (may be "Unknown Workspace" if missing)
  worktreeId?: string; // Original worktree ID if available
  chats: ChatWithActivity[];
  isWorktreeArchived: boolean; // True if the worktree itself is archived
}

interface ChatWithActivity extends Chat {
  activityState: ChatActivityState;
  priority: number;
  lastActivity?: string;
}

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
  const showStatusDot = chat.activityState !== "idle" && chat.activityState !== "awaiting_approval";
  const showNotificationBadge = chat.unread || chat.activityState === "awaiting_approval";
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
          : showStatusDot
            ? "border-success/30 bg-success/5 text-foreground hover:bg-success/10"
            : "bg-transparent text-foreground/80 hover:bg-muted/50 hover:text-foreground",
        "active:scale-[0.99]"
      )}
      onClick={() => onChatClick(chat)}
      onContextMenu={(e) => onContextMenu(e, chat)}
    >
      {showNotificationBadge && (
        <Tooltip 
          content={chat.activityState === "awaiting_approval" ? "Approval needed" : "New activity"} 
          placement="top" 
          delay={300}
        >
          <div className="absolute right-1.5 top-1.5 h-2 w-2 rounded-full bg-destructive ring-2 ring-card" />
        </Tooltip>
      )}

      {showStatusDot && (
        <div 
          className="flex-shrink-0 group-hover:scale-110 transition-transform duration-200"
          data-testid={`chat-activity-dot-${chat.id}`}
          data-activity-state={chat.activityState}
        >
          <ActivityDot state={chat.activityState} />
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
          <div className="truncate group-hover:text-foreground transition-colors duration-200">
            {chatTitle}
          </div>
        )}
        {!inGroup && workspaceBranch && (
          <div className="flex items-center gap-1 mt-0.5">
            <GitBranch className="w-2.5 h-2.5 text-muted-foreground/60" />
            <span className="text-[10px] text-muted-foreground/60 truncate max-w-[120px]">
              {workspaceBranch}
            </span>
          </div>
        )}
      </div>

      <div className="flex-shrink-0 opacity-0 group-hover:opacity-100 transition-opacity duration-200">
        <Tooltip content="More options" placement="left" delay={300}>
          <button
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
        <Tooltip content="Archive chat" placement="left" delay={300}>
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

// --- Extracted ArchivedChatItem (module-level for stable identity + React.memo) ---

interface ArchivedChatItemProps {
  chat: ChatWithActivity;
  activeChatId: string | null;
}

const ArchivedChatItem = memo(function ArchivedChatItem({ chat, activeChatId }: ArchivedChatItemProps) {
  const chatId = chat.id;
  const chatTitle = chat.title || "New chat";
  const isActive = activeChatId === chatId;
  const relativeTime = getRelativeTime(chat.updatedAt || chat.createdAt);
  const deleteChatMutation = useDeleteChat();
  const unarchiveMutation = useUnarchiveChat();

  const handleViewChat = async () => {
    const currentProject = useProjectStore.getState().currentProject;
    const worktreeStore = useWorktreeStore.getState();

    if (currentProject?.id) {
      if (chat.worktreeId) {
        const targetWorktree = worktreeStore.worktrees.find((worktree) => worktree.id === chat.worktreeId) ?? null;
        if (targetWorktree) {
          await worktreeStore.switchWorktreeContext(currentProject.id, targetWorktree);
        }
      } else {
        await worktreeStore.switchWorktreeContext(currentProject.id, null);
      }
    }

    const { selectChat } = useChatStore.getState();
    selectChat(chat);
    const { navigateToChat } = useChatNavigationStore.getState();
    navigateToChat(chatId);
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
      </div>

      <div className="flex-shrink-0 opacity-0 group-hover:opacity-100 transition-opacity duration-200">
        <Tooltip content="Restore chat" placement="left" delay={300}>
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
        <Tooltip content="Delete chat" placement="left" delay={300}>
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
}: WorktreeGroupComponentProps) {
  return (
    <div className="mb-1.5">
      {/* Worktree Container */}
      <div
        className={cn(
          "overflow-hidden rounded-lg border transition-all duration-150",
          hasActiveChat
            ? "border-primary/20 bg-primary/10"
            : "border-transparent hover:border-border/50 hover:bg-muted/30"
        )}
      >
        {/* Header */}
        <div
          className="flex w-full items-center gap-1.5 px-2.5 py-1.5 group/header"
          onContextMenu={(e) => onContextMenu?.(e)}
        >
          <button
            onClick={onToggle}
            className="flex min-w-0 flex-1 items-center gap-2 rounded-md text-left transition-all duration-150"
          >
            {icon}

            <div className="flex min-w-0 flex-1 items-center gap-1.5 text-left">
              <div
                className={cn(
                  "min-w-0 flex-1 truncate text-[11px] font-semibold uppercase tracking-wide",
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
                "rounded-full px-1.5 py-0.5 text-[10px] font-medium",
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
              placement="left"
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
              placement="left"
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
          <div className="space-y-0.5 px-1.5 pb-1.5 pt-0.5">
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
              "mx-2 mb-2 w-auto rounded-md p-2 text-left text-xs text-muted-foreground transition-colors",
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
  onNavigateToProjectPicker,
  onOpenWorkflows,
  onOpenChatSearch,
  onNavigateToSettings,
}: SidebarProps) {
  const currentProject = useProjectStore((state) => state.currentProject);
  const { data: chats = [] } = useChatList(currentProject?.id);
  // Activity from activityStore (SINGLE SOURCE OF TRUTH)
  const selectChat = useChatStore((state) => state.selectChat);
  const deleteChatMutation = useDeleteChat();
  const renameChatMutation = useRenameChat();
  const markUnreadMutation = useMarkUnread();
  const unarchiveMutation = useUnarchiveChat();
  const activities = useActivityStore((state) => state.activities);
  // Archived chats from React Query (auto-fetches)
  const { data: archivedChats = [] } = useArchivedChats();
  
  const worktrees = useWorktreeStore((state) => state.worktrees);
  const switchWorktreeContext = useWorktreeStore((state) => state.switchWorktreeContext);
  const fetchProcesses = useProcessStore(
    (state) => state.fetchProcesses
  );

  // Get active chat from navigation store
  const activeChatId = useChatStore((state) => state.activeChatId);

  // Chat list preferences
  const sortOrder = useChatListPreferencesStore((state) => state.sortOrder);
  const viewMode = useChatListPreferencesStore((state) => state.viewMode);
  const setSortOrder = useChatListPreferencesStore((state) => state.setSortOrder);
  const setViewMode = useChatListPreferencesStore((state) => state.setViewMode);

  // UI State
  const [isSortMenuOpen, setIsSortMenuOpen] = useState(false);
  const [isViewMenuOpen, setIsViewMenuOpen] = useState(false);
  const [chatListTab, setChatListTab] = useState<"active" | "archived">("active");

  const [expandedWorktreeGroups, setExpandedWorktreeGroups] = useState<
    Record<string, boolean>
  >({});
  const [isNoWorktreeExpanded, setIsNoWorktreeExpanded] = useState(true);
  const [isArchivedExpanded, setIsArchivedExpanded] = useState(false);
  const [expandedArchivedGroups, setExpandedArchivedGroups] = useState<
    Record<string, boolean>
  >({});
  const [contextMenu, setContextMenu] = useState<
    | {
        x: number;
        y: number;
        type: "chat";
        chatId: string;
      }
    | {
        x: number;
        y: number;
        type: "worktree";
        worktreeId: string;
      }
    | null
  >(null);
  const [editingChatId, setEditingChatId] = useState<string | null>(null);
  const [editingTitle, setEditingTitle] = useState("");

  // Ref for scroll container
  const scrollContainerRef = useRef<HTMLDivElement>(null);

  // Filter archived chats by current project
  const projectArchivedChats = useMemo(() => {
    if (!currentProject) return [];
    return archivedChats.filter((c) => c.projectId === currentProject.id);
  }, [archivedChats, currentProject]);

  // Initialize background tasks monitoring for all chats
  // Fetch processes once on mount - subsequent updates come through gRPC stream
  // (process_started, process_completed, process_failed events in globalUpdatesStore)
  const hasInitializedProcesses = useRef(false);
  useEffect(() => {
    if (!hasInitializedProcesses.current && chats.length > 0) {
      hasInitializedProcesses.current = true;
      fetchProcesses();
    }
  }, [chats.length, fetchProcesses]);

  // Activity detection - reads directly from activityStore (SINGLE SOURCE OF TRUTH)
  const getChatActivityState = useCallback(
    (chat: Chat): ChatActivityState => {
      return activityToDotState(activities.get(chat.id) ?? ChatActivity.IDLE);
    },
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
      const activityState = getChatActivityState(chat);

      // Priority calculation for sorting (only truly active chats float to top)
      let priority = 0;
      if (activityState === "awaiting_approval") priority += 1000; // Highest - needs user action
      if (activityState === "thinking" || activityState === "streaming")
        priority += 800; // AI actively working
      if (activityState === "error") priority += 400; // Errors need attention
      // Unread chats get a boost (but lower than active work)
      if (chat.unread) priority += 200;

      return {
        ...chat,
        activityState,
        priority,
        lastActivity: chat.updatedAt,
      };
    });
  }, [chats, getChatActivityState]);

  // Get worktree for a chat - all chats now have worktree_id
  const getWorktreeForChat = useCallback(
    (chat: Chat) => {
      return worktrees.find((w) => w.id === chat.worktreeId);
    },
    [worktrees]
  );

  // Filtered and sorted chats grouped by worktree (or flat list)
  const { activeGroups, flatList, archivedChats: filteredArchivedChats, archivedGroups } = useMemo(() => {
    const filtered = chatsWithActivity;

    // Categorize chats - ONLY use active (non-archived) chats from the main list
    // All chats now have worktree_id, no null handling needed
    const activeWorktreeChats: ChatWithActivity[] = [];

    filtered.forEach((chat) => {
      const worktree = getWorktreeForChat(chat);

      // Skip if worktree doesn't exist (these are archived)
      if (!worktree) {
        return;
      }

      // Skip if worktree is completed/abandoned (these are archived)
      if (
        worktree.status === WorktreeStatus.COMPLETED ||
        worktree.status === WorktreeStatus.ABANDONED
      ) {
        return;
      }

      // Active worktree → Active Chats
      activeWorktreeChats.push(chat);
    });

    // Convert archived chats from store to ChatWithActivity format
    const archivedList: ChatWithActivity[] = projectArchivedChats.map((chat) => ({
      ...chat,
      activityState: "idle" as const,
      priority: 0,
      lastActivity: chat.updatedAt,
    }));

    // Group active chats by worktree - simple grouping now that all chats have worktree_id
    const worktreeGroupsMap = new Map<string, ChatGroup>();

    activeWorktreeChats.forEach((chat) => {
      const worktree = getWorktreeForChat(chat);
      if (!worktree) return;

      if (!worktreeGroupsMap.has(worktree.id)) {
        worktreeGroupsMap.set(worktree.id, {
          worktreeId: worktree.id,
          worktreeName: worktree.name,
          worktreeBranch: worktree.branch,
          chats: [],
          hasActivity: false,
          isMain: worktree.is_main,
        });
      }

      const group = worktreeGroupsMap.get(worktree.id)!;
      group.chats.push(chat);

      // Check if any chat in group requires immediate action (awaiting_approval)
      // Only awaiting_approval should cause groups to float - other activity states
      // get visual indicators but don't change sort order
      if (chat.activityState === "awaiting_approval") {
        group.hasActivity = true;
      }
    });

    // Sort chats based on user preference
    const sortChatList = (chats: ChatWithActivity[]) => {
      return [...chats].sort((a, b) => {
        // ONLY awaiting_approval floats to top - it requires immediate user action
        // Other states (thinking, streaming) stay in their natural position but get visual indicators
        const aRequiresAction = a.activityState === "awaiting_approval";
        const bRequiresAction = b.activityState === "awaiting_approval";

        if (aRequiresAction && !bRequiresAction) return -1;
        if (!aRequiresAction && bRequiresAction) return 1;

        // Apply user-selected sort order for all other chats
        switch (sortOrder) {
          case "recent_activity":
            // Most recent last_message_at first (falls back to created_at for new chats)
            // Using last_message_at prevents "viewing" a chat from changing its sort position
            return (
              new Date(b.lastMessageAt || b.createdAt).getTime() -
              new Date(a.lastMessageAt || a.createdAt).getTime()
            );
          case "needs_attention_first": {
            // Chats needing attention come first (unread or awaiting_approval)
            // This is an explicit sort mode where user WANTS these at top
            const aNeedsAttention = a.unread || a.activityState === "awaiting_approval";
            const bNeedsAttention = b.unread || b.activityState === "awaiting_approval";
            
            if (aNeedsAttention && !bNeedsAttention) return -1;
            if (!aNeedsAttention && bNeedsAttention) return 1;
            
            // Both need attention or neither - sort by last_message_at
            return (
              new Date(b.lastMessageAt || b.createdAt).getTime() -
              new Date(a.lastMessageAt || a.createdAt).getTime()
            );
          }
          case "newest_first":
            // Most recent created_at first
            return (
              new Date(b.createdAt).getTime() -
              new Date(a.createdAt).getTime()
            );
          case "oldest_first":
            // Oldest created_at first
            return (
              new Date(a.createdAt).getTime() -
              new Date(b.createdAt).getTime()
            );
          case "alphabetical_asc":
            // A-Z by title
            return (a.title || "New chat").localeCompare(
              b.title || "New chat"
            );
          case "alphabetical_desc":
            // Z-A by title
            return (b.title || "New chat").localeCompare(
              a.title || "New chat"
            );
          default:
            return 0;
        }
      });
    };

    // Sort chats within each group
    worktreeGroupsMap.forEach((group) => {
      group.chats = sortChatList([...group.chats]);
    });

    // Ensure main workspace is always present in grouped view, even with zero chats
    const mainWorktree = worktrees.find(
      (w) =>
        w.is_main &&
        !w.deleted_at &&
        w.status !== WorktreeStatus.COMPLETED &&
        w.status !== WorktreeStatus.ABANDONED
    );

    if (mainWorktree && !worktreeGroupsMap.has(mainWorktree.id)) {
      worktreeGroupsMap.set(mainWorktree.id, {
        worktreeId: mainWorktree.id,
        worktreeName: mainWorktree.name,
        worktreeBranch: mainWorktree.branch,
        chats: [],
        hasActivity: false,
        isMain: true,
      });
    }

    // Convert to array and sort groups (groups with activity first, then by most recent chat)
    const groupsArray = Array.from(worktreeGroupsMap.values());
    groupsArray.sort((a, b) => {
      // Main workspace should be first when it has no chats so users can always switch to it
      if (a.isMain && a.chats.length === 0) return -1;
      if (b.isMain && b.chats.length === 0) return 1;

      // Active groups first
      if (a.hasActivity !== b.hasActivity) {
        return a.hasActivity ? -1 : 1;
      }

      // If one group is empty, put non-empty first (except main empty special-case above)
      if (a.chats.length === 0 && b.chats.length > 0) return 1;
      if (b.chats.length === 0 && a.chats.length > 0) return -1;

      // Then by most recent chat in group (using last_message_at for activity-based sorting)
      const aTime = a.chats.length > 0
        ? Math.max(...a.chats.map((c) => new Date(c.lastMessageAt || c.createdAt).getTime()))
        : 0;
      const bTime = b.chats.length > 0
        ? Math.max(...b.chats.map((c) => new Date(c.lastMessageAt || c.createdAt).getTime()))
        : 0;
      return bTime - aTime;
    });

    // Create flat list for flat view mode
    const flatList = sortChatList(activeWorktreeChats);

    // Group archived chats by worktree_name (similar to active chats grouping)
    const archivedGroupsMap = new Map<string, ArchivedChatGroup>();

    archivedList.forEach((chat) => {
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

    // Convert to array and sort groups by most recent chat
    const archivedGroupsArray = Array.from(archivedGroupsMap.values());
    archivedGroupsArray.sort((a, b) => {
      const aTime = Math.max(
        ...a.chats.map((c) => new Date(c.lastMessageAt || c.createdAt).getTime())
      );
      const bTime = Math.max(
        ...b.chats.map((c) => new Date(c.lastMessageAt || c.createdAt).getTime())
      );
      return bTime - aTime;
    });

    return {
      activeGroups: groupsArray,
      flatList,
      archivedChats: sortChatList([...archivedList]),
      archivedGroups: archivedGroupsArray,
    };
  }, [
    chatsWithActivity,
    getWorktreeForChat,
    projectArchivedChats,
    sortOrder,
    worktrees,
  ]);

  const handleNewChat = async () => {
    // Clear active chat to show new chat view
    // Preserve current worktree context from active chat or current worktree store
    const worktreeStore = useWorktreeStore.getState();
    const activeChat = activeChatId ? chats.find(c => c.id === activeChatId) ?? null : null;
    const currentWorktreeId = activeChat?.worktreeId || worktreeStore.currentWorktree?.id || null;
    useChatStore.getState().clearCurrentChat(currentWorktreeId);
  };

  const handleSwitchToWorktreeNewChat = useCallback(
    async (worktreeId: string) => {
      if (!currentProject?.id) return;

      const targetWorktree = worktrees.find((w) => w.id === worktreeId);
      if (!targetWorktree) return;

      await switchWorktreeContext(currentProject.id, targetWorktree);

      // Show New Chat page with the target workspace selected
      useChatStore.getState().clearCurrentChat(targetWorktree.id);
    },
    [currentProject?.id, worktrees, switchWorktreeContext]
  );

  const handleChatClick = useCallback(async (chat: Chat) => {
    // Switch worktree context first to save/restore viewer state
    if (chat.worktreeId && currentProject?.id) {
      const worktree = worktrees.find(w => w.id === chat.worktreeId);
      if (worktree) {
        await switchWorktreeContext(currentProject.id, worktree);
      }
    } else if (currentProject?.id) {
      await switchWorktreeContext(currentProject.id, null);
    }
    
    selectChat(chat);
    const { navigateToChat } = useChatNavigationStore.getState();
    navigateToChat(chat.id);
  }, [currentProject?.id, worktrees, switchWorktreeContext, selectChat]);

  const handleChatContextMenu = useCallback((e: React.MouseEvent, chat: Chat) => {
    e.preventDefault();
    e.stopPropagation();
    setContextMenu({ x: e.clientX, y: e.clientY, type: "chat", chatId: chat.id });
  }, []);

  const handleWorktreeContextMenu = useCallback((e: React.MouseEvent, worktreeId: string) => {
    e.preventDefault();
    e.stopPropagation();

    const worktree = worktrees.find((w) => w.id === worktreeId);
    if (!worktree?.path) return;

    setContextMenu({
      x: e.clientX,
      y: e.clientY,
      type: "worktree",
      worktreeId,
    });
  }, [worktrees]);

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

  const getChatContextMenuItems = (chatId: string): ContextMenuItem[] => {
    const chat = chats.find((c) => c.id === chatId);
    if (!chat) return [];

    const menuItems: ContextMenuItem[] = [
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

    const chatWorktree = worktrees.find((w) => w.id === chat.worktreeId);
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

    menuItems.push(
      { label: "", onClick: () => {}, separator: true },
      {
        label: "Delete",
        icon: <Trash2 className="w-4 h-4" />,
        onClick: () => deleteChatMutation.mutate(chatId),
        danger: true,
      }
    );

    return menuItems;
  };

  const getWorktreeContextMenuItems = (worktreeId: string): ContextMenuItem[] => {
    const worktree = worktrees.find((w) => w.id === worktreeId);
    if (!worktree?.path) return [];

    return [
      {
        label: "Copy Workspace Path",
        icon: <Copy className="w-4 h-4" />,
        onClick: () => copyToClipboard(worktree.path),
      },
    ];
  };

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

  // Scroll active chat into view when it changes (e.g., via keyboard navigation)
  // NOTE: Only trigger when activeChatId changes, NOT when chats list changes
  // This prevents scrolling when unarchiving chats
  useEffect(() => {
    if (!activeChatId || !scrollContainerRef.current) return;

    // Access current chats list inside the effect (not in dependencies)
    // This way the effect only runs when activeChatId changes
    const currentChatsWithActivity = chatsWithActivity;
    const currentProjectArchivedChats = projectArchivedChats;
    const currentArchivedGroups = archivedGroups;

    // Find the chat in the active list first
    let chat = currentChatsWithActivity.find((c) => c.id === activeChatId);
    let isArchived = false;

    // If not found in active chats, check archived chats
    if (!chat) {
      const archivedChat = currentProjectArchivedChats.find(
        (c) => c.id === activeChatId
      );
      if (archivedChat) {
        chat = {
          ...archivedChat,
        } as ChatWithActivity;
        isArchived = true;
      }
    }

    if (!chat) return;

    if (isArchived) {
      setChatListTab("archived");
      if (!isArchivedExpanded) {
        setIsArchivedExpanded(true);
      }
    } else {
      setChatListTab("active");
    }

    // If in grouped view and not archived, expand the worktree group containing this chat
    if (!isArchived && viewMode === "grouped") {
      const worktree = getWorktreeForChat(chat);
      if (worktree) {
        const worktreeId = worktree.id;
        const isNoWorktree = worktreeId === "no-worktree" || !worktreeId;
        
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
      const containingGroup = currentArchivedGroups.find((group) =>
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
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [
    activeChatId,
    // Only include viewMode and expansion state - NOT the chats lists
    // This prevents the effect from running when chats are unarchived
    viewMode,
    expandedWorktreeGroups,
    expandedArchivedGroups,
    isNoWorktreeExpanded,
    isArchivedExpanded,
    getWorktreeForChat,
  ]);


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

  const renderWorktreeGroup = useCallback(
    (group: ChatGroup) => {
      // Handle main branch (no worktree) special case
      const isNoWorktree = group.worktreeId === "no-worktree";
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
            void handleSwitchToWorktreeNewChat(group.worktreeId);
          };

      const handleArchiveWorktree = (isNoWorktree || group.isMain)
        ? undefined
        : async () => {
            const { archiveWorktree } = useWorktreeStore.getState();
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
                  void handleSwitchToWorktreeNewChat(group.worktreeId);
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
          icon={worktreeIcon}
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
      handleChatClick,
      handleChatContextMenu,
      handleSaveRename,
      handleArchiveChat,
      handleSwitchToWorktreeNewChat,
      handleCancelRename,
      handleWorktreeContextMenu,
    ]
  );

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
        <Tooltip content="Workspace archived" placement="right" delay={300}>
          <Archive className="w-3 h-3 text-muted-foreground/60" />
        </Tooltip>
      ) : null;

      const handleWorktreeMenu = group.worktreeId
        ? (e: React.MouseEvent) => handleWorktreeContextMenu(e, group.worktreeId as string)
        : undefined;

      return (
        <WorktreeGroupComponent
          key={groupKey}
          id={groupKey}
          title={group.worktreeName}
          subtitle={subtitle}
          icon={
            <FolderGit2 className="w-3.5 h-3.5 text-muted-foreground/60 flex-shrink-0" />
          }
          chats={group.chats}
          isExpanded={isExpanded}
          onToggle={handleToggle}
          chatCount={group.chats.length}
          renderChat={(chat) => <ArchivedChatItem chat={chat} activeChatId={activeChatId} />}
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
    <div className="flex h-full flex-col bg-card dense-ui" data-onboarding="left-sidebar">
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
          />
          <SidebarNavButton
            icon={<FolderOpen className="h-4 w-4" />}
            label="Projects"
            onClick={onNavigateToProjectPicker}
          />
          <SidebarNavButton
            icon={<Workflow className="h-4 w-4" />}
            label="Workflows"
            onClick={onOpenWorkflows}
          />
          <SidebarNavButton
            icon={<Search className="h-4 w-4" />}
            label="Search"
            onClick={onOpenChatSearch}
          />
        </nav>
      </div>

      <div className="border-b border-border/40 bg-card px-3 py-2">
        <div className="flex items-center gap-1.5">
          <div className="flex min-w-0 flex-1 items-center gap-3">
            <button
              type="button"
              onClick={() => setChatListTab("active")}
              className={cn(
                "text-xs font-medium transition-colors focus:outline-none focus:ring-2 focus:ring-ring/40",
                chatListTab === "active" ? "text-foreground" : "text-muted-foreground hover:text-foreground"
              )}
              aria-pressed={chatListTab === "active"}
            >
              Active
            </button>
            <button
              type="button"
              onClick={() => setChatListTab("archived")}
              className={cn(
                "text-xs font-medium transition-colors focus:outline-none focus:ring-2 focus:ring-ring/40",
                chatListTab === "archived" ? "text-foreground" : "text-muted-foreground hover:text-foreground"
              )}
              aria-pressed={chatListTab === "archived"}
            >
              Archived
            </button>
          </div>

          <Dropdown
            isOpen={isSortMenuOpen}
            onOpenChange={setIsSortMenuOpen}
            align="right"
            trigger={
              <Tooltip
                content={`Sort: ${currentSortOption.label}`}
                placement="bottom"
                delay={300}
                wrapperClassName={CHAT_HEADER_ACTION_TOOLTIP_CLASS}
              >
                <button
                  type="button"
                  onClick={() => setIsSortMenuOpen(!isSortMenuOpen)}
                  className={cn(
                    CHAT_HEADER_ACTION_BUTTON_CLASS,
                    "text-muted-foreground hover:bg-muted/70 hover:text-foreground"
                  )}
                  aria-label="Sort chats"
                >
                  {currentSortOption.icon}
                </button>
              </Tooltip>
            }
            contentClassName="min-w-[190px]"
          >
            <div className="py-1">
              <div className="px-3 py-1.5 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">
                Sort by
              </div>
              {SORT_OPTIONS.map((option) => (
                <button
                  key={option.value}
                  onClick={() => setSortOrder(option.value)}
                  className={cn(
                    "flex w-full items-center justify-between gap-2 rounded-sm px-3 py-2 text-xs transition-colors hover:bg-muted/50",
                    sortOrder === option.value ? "text-foreground" : "text-muted-foreground hover:text-foreground"
                  )}
                >
                  <div className="flex items-center gap-2">
                    <span className="flex h-3.5 w-3.5 items-center justify-center">{option.icon}</span>
                    <span>{option.label}</span>
                  </div>
                  {sortOrder === option.value && <Check className="h-3.5 w-3.5 text-primary" />}
                </button>
              ))}
            </div>
          </Dropdown>

          <Dropdown
            isOpen={isViewMenuOpen}
            onOpenChange={setIsViewMenuOpen}
            align="right"
            trigger={
              <Tooltip
                content="View options"
                placement="bottom"
                delay={300}
                wrapperClassName={CHAT_HEADER_ACTION_TOOLTIP_CLASS}
              >
                <button
                  type="button"
                  onClick={() => setIsViewMenuOpen(!isViewMenuOpen)}
                  className={cn(
                    CHAT_HEADER_ACTION_BUTTON_CLASS,
                    "text-muted-foreground hover:bg-muted/70 hover:text-foreground"
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
            }
            contentClassName="min-w-[220px]"
          >
            <div className="py-1">
              <div className="px-3 py-1.5 text-[10px] font-semibold uppercase tracking-wider text-muted-foreground">
                View
              </div>
              <button
                onClick={() => setViewMode("grouped")}
                className={cn(
                  "flex w-full items-center justify-between gap-2 rounded-sm px-3 py-2 text-xs transition-colors hover:bg-muted/50",
                  viewMode === "grouped" ? "text-foreground" : "text-muted-foreground hover:text-foreground"
                )}
              >
                <div className="flex items-center gap-2">
                  <LayoutGrid className="h-3.5 w-3.5" />
                  <span>Grouped by workspace</span>
                </div>
                {viewMode === "grouped" && <Check className="h-3.5 w-3.5 text-primary" />}
              </button>
              <button
                onClick={() => setViewMode("flat")}
                className={cn(
                  "flex w-full items-center justify-between gap-2 rounded-sm px-3 py-2 text-xs transition-colors hover:bg-muted/50",
                  viewMode === "flat" ? "text-foreground" : "text-muted-foreground hover:text-foreground"
                )}
              >
                <div className="flex items-center gap-2">
                  <LayoutList className="h-3.5 w-3.5" />
                  <span>Flat list</span>
                </div>
                {viewMode === "flat" && <Check className="h-3.5 w-3.5 text-primary" />}
              </button>
            </div>
          </Dropdown>

        </div>
      </div>

      <div className="flex flex-1 flex-col overflow-hidden">
        <div ref={scrollContainerRef} className="min-h-0 flex-1 overflow-y-auto px-2 py-2">
          {chatListTab === "active" ? (
            viewMode === "grouped" ? (
              activeGroups.length > 0 && (
                <div className="space-y-1.5">
                  {activeGroups.map((group) => renderWorktreeGroup(group))}
                </div>
              )
            ) : (
              flatList.length > 0 && (
                <div className="space-y-1">
                  {flatList.map((chat) => {
                    const worktree = getWorktreeForChat(chat);
                    return (
                      <ChatItem
                        key={chat.id}
                        chat={chat}
                        inGroup={false}
                        editingChatId={editingChatId}
                        editingTitle={editingTitle}
                        workspaceBranch={worktree?.branch}
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
                  <ArchivedChatItem key={chat.id} chat={chat} activeChatId={activeChatId} />
                ))}
              </div>
            )
          ) : null}

          {/* Empty State */}
          {!hasAnyVisibleChats && (
            <div className="py-8 text-center text-muted-foreground">
              <div className="mx-auto mb-3 flex h-12 w-12 items-center justify-center rounded-full border border-border/50 bg-muted/40">
                {chatListTab === "active" ? <Plus className="h-5 w-5" /> : <Archive className="h-5 w-5" />}
              </div>
              <p className="mb-1 text-sm font-medium">{emptyTitle}</p>
              <p className="text-xs text-muted-foreground/70">{emptyDescription}</p>
              {chatListTab === "active" && (
                <div className="mt-3 flex flex-col items-center gap-2">
                  <Button
                    onClick={handleNewChat}
                    variant="ghost"
                    size="sm"
                  >
                    Create Chat
                  </Button>
                  {viewMode === "flat" && activeGroups.length > 0 && (
                    <Button
                      onClick={() => {
                        const mainGroup = activeGroups.find((g) => g.isMain);
                        if (mainGroup) {
                          void handleSwitchToWorktreeNewChat(mainGroup.worktreeId);
                        }
                      }}
                      variant="ghost"
                      size="sm"
                    >
                      Go to Main Workspace
                    </Button>
                  )}
                </div>
              )}
            </div>
          )}
        </div>
      </div>

      <div className="border-t border-border/40 bg-card px-3 py-2">
        <SidebarNavButton
          icon={<Settings className="h-4 w-4" />}
          label="Settings"
          onClick={onNavigateToSettings}
        />
      </div>

      {/* Context Menu */}
      {contextMenu && (
        <ContextMenu
          items={
            contextMenu.type === "chat"
              ? getChatContextMenuItems(contextMenu.chatId)
              : getWorktreeContextMenuItems(contextMenu.worktreeId)
          }
          position={{ x: contextMenu.x, y: contextMenu.y }}
          onClose={() => setContextMenu(null)}
        />
      )}
      {/* Render complete */}
    </div>
  );
}

export const Sidebar = SidebarComponent;
