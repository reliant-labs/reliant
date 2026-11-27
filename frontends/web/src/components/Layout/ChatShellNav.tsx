// Copyright (c) 2026 Reliant Labs

/**
 * The shell's nav column with the chats in it: the one column
 * (sidebar/ShellNavColumn.tsx) and the live slots only the chat shell can
 * draw (New chat, the chat list and its menus and dialogs, the Inbox count,
 * the Workflows pill, the Agents dot), for whichever config its host passes.
 *
 * Two hosts draw it. The app's nav (Layout/Sidebar.tsx) passes its config
 * (navConfig/hybrid.ts), its own footer slots (Help, the update button) and
 * whether chats can run yet; the public demo's (Forge/Demo/DemoNav.tsx)
 * passes the demo's config and its phone actions. So this module imports
 * neither config, nor the app's onboarding or Help: what only one host has,
 * that host hands in.
 */

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
import {
  activeWorkspaceChats,
  groupByProject,
  groupByWorkspace,
  isProjectHiddenFromSidebar,
  listProjectGroups,
  type WorkspaceGroup,
} from "../../lib/sidebarGroups";
import { useSidebarProjectPrefs } from "../../hooks/sidebar-project-queries";
import {
  Plus,
  Trash2,
  Archive,
  Edit,
  Copy,
  RotateCcw,
  Mail,
  GitBranch,
  SquarePen,
  Activity,
  FolderInput,
  Pin,
  PinOff,
  Eye,
  EyeOff,
  MoreHorizontal,
  Users,
  Workflow as WorkflowIcon,
} from "lucide-react";
import { useChatStore } from "../../store/chatStore";
import { useChat, useChatListWithWorkspaces, useArchivedChats, useArchiveChat, usePermanentlyDeleteChat, useRenameChat, useUnarchiveChat } from "../../hooks/chat-queries";
import { useAdoptRun, useUnadoptRun, type LiveRunsScope } from "../../hooks/run-queries";
import { useTriggerName } from "../../hooks/trigger-queries";
import { isAdoptedAutomation, isRun, withOpenChatPinned } from "../../lib/sidebarChatList";
import { useSidebarPinStore } from "../../store/sidebarPinStore";
import { LaunchKindIcon } from "../runs/LaunchKindIcon";
import { AutomationActivityPill } from "./AutomationActivityPill";
import { InboxNavItem } from "../inbox/InboxNavItem";
import { SharedWithOrgSection } from "../Sharing/SharedWithOrgSection";
import { ShareChatDialog } from "../Sharing/ShareChatDialog";
import { ShareWorkflowDialog } from "../Sharing/ShareWorkflowDialog";
import { canShareChat, type ShareableWorkflow } from "../Sharing/shareTargets";
import { useChatWorkflowShare } from "../Sharing/useChatWorkflowShare";
import { useChatProjectFilter } from "../../hooks/useChatProjectFilter";
import type { RunsSearch } from "../../routeSchemas";
import { useMarkUnread } from "../../hooks/message-queries";
import { activityNeedsAttention } from "../../store/chatNavigationStore";
import { useProjectStore } from "../../store/projectStore";
import { useWorkspaceStateStore } from "../../store/workspaceStateStore";
import { openChat, openNewChat } from "../../store/openChat";
import { openAddProject } from "../../store/addProjectDialogStore";
import { useProjectDaemonInstalls, useProjects } from "../../hooks/project-queries";
import { ChatListMenu } from "./ChatListMenu";
import { MachineFilterMenu, machineFilterLabel } from "./MachineFilterMenu";
import { useDaemonList } from "../../hooks/useOnboardingQueries";
import { useMachineOrgs } from "../../hooks/useMachineOrgs";
import { ALL_PROJECTS, useCachedWorktrees } from "../../hooks/worktree-queries";
import {
  countByMachine,
  effectiveMachineFilter,
  filterChatsByMachine,
  filterProjectsByMachine,
  machineResolver,
  NO_MACHINE,
  type MachineFilter,
} from "../../lib/sidebarMachineFilter";
import { ProjectMark } from "./ProjectMark";
import { useShortcutLabel } from "../../store/shortcutsStore";
import { toast } from "../../lib/toast-manager";
import Tooltip from "../forge-ui/tooltip";
import Button from "@/components/forge-ui/button";
import ConfirmationDialog from "@/components/forge-ui/confirmation_dialog";
import { createPortal } from "react-dom";
import ContextMenu from "../forge-ui/context_menu";
import type { MenuGroup } from "../forge-ui/dropdown_menu";
import { contextMenuOpening, type ContextMenuOpening } from "../../lib/contextMenuOpening";
import { focusChatInput, isInputFocused } from "../../hooks/useFocusManager";
import { focusEventName } from "../../lib/keyboard/focus";
import { inboxPath, newChatPath, projectsPath, withSearch } from "../../lib/paths";
import type { Chat } from "../../api/client";
import { useChatListPreferencesStore } from "../../store/chatListPreferencesStore";
import { sortChats, compareChatGroups } from "../../lib/chatListOrder";
import { cn } from "../../lib/utils";
import DropdownMenu from "../forge-ui/dropdown_menu";
import {
  ChatRow,
  ChatStateSlot,
  ProjectGroupHeader,
  SIDEBAR_HEADING_HOVER_ACTION,
  SIDEBAR_ICON_BUTTON,
  SidebarSubRow,
  ChatSectionHeading,
} from "./sidebar/primitives";
import { usePublishShellScope, useShellProject, type ShellProject } from "./sidebar/shellProject";
import type { NavigateOptions, NavSlots } from "./sidebar/NavRenderer";
import type { NavConfig } from "./sidebar/navConfig/types";
import { ShellNavColumn } from "./sidebar/ShellNavColumn";
import { useNarrowViewport } from "../../hooks/useNarrowViewport";
import { isAtOrBelow } from "./sidebar/navLocation";
import { chatListIndicators, launchKindDisplay } from "../../lib/runStatus";
import { chatRowChip } from "./chatRowStatus";
import {
  useActivityStore,
  activityToDotState,
  ChatActivity,
  type DotState as ChatActivityState,
} from "../../store/activityStore";

import { archiveWorktree } from "../../hooks/worktree-actions";
import { MoveChatToProjectDialog } from "../Chat/MoveChatToProjectDialog";
import { ChatAppTag } from "../Projects/ChatAppTag";
import { ResourceVisibility } from "../../gen/reliant/v1/common_pb";

/**
 * Chats cannot run yet (OnboardingFlow/onboardingGatePolicy.ts, ChatSetup):
 * where New chat goes instead, and the card the Chats section shows in place
 * of the list. The host decides (Layout/Sidebar.tsx), so this module never
 * imports the app's onboarding.
 */
export interface ChatSetupGate {
  href: string;
  card: ReactNode;
}

export interface ChatShellNavProps {
  /** The page on screen (the router's): which row is current, which app is open. */
  pathname?: string;
  /** The page's search, carried by the rows that keep a scope (`?project=`, `?env=`). */
  search?: Record<string, unknown>;
  /** Opens a page: the shell's navigate (Layout/shell/AppShell.tsx); `drill` keeps a drawer open. */
  onNavigate?: (href: string, options?: NavigateOptions) => void;
  /** Open the Runs tab with filters; the Workflows row's pill passes "live". */
  onOpenRuns?: (search?: RunsSearch) => void;
  onOpenChatSearch?: () => void;
  /** The nav to draw: the app's `hybridNav` (Layout/Sidebar.tsx), or a host's own (Forge/Demo/demoNavConfig.ts). */
  config: NavConfig;
  /**
   * The host's own slots, beside the chat slots this draws (new-chat,
   * chat-list, inbox, workflows-pill, chats-need-you): the app's Help and
   * update button, the public demo's phone actions.
   */
  hostSlots?: NavSlots;
  /** Set while chats cannot run (ChatSetupGate); null or omitted, they can. */
  chatSetup?: ChatSetupGate | null;
  /** Find under the org row (ShellNavColumn `find`); on by default. */
  find?: boolean;
  /**
   * A host's own reading of the scope, over the route's (sidebar/shellProject.ts):
   * `pathname` is the page the scope is read from (the page on screen
   * when omitted), `adjust` rewrites what it reads. The public demo reads
   * its own pages as the sample shop's and always opens the shop's sections.
   */
  scope?: { pathname?: string; adjust?: (shell: ShellProject) => ShellProject };
}

const NO_SEARCH: Record<string, unknown> = {};

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

/** A chat with no title yet. Not "New chat": that is the button above the list. */
const UNTITLED_CHAT = "Untitled chat";

/** The archived list before its tab is opened: one stable empty array. */
const NO_ARCHIVED_CHATS: Chat[] = [];

/**
 * Up/Down (and Home/End) move focus between chat rows, in the order they are
 * drawn, across groups. Enter or Space on a row opens it (it is a button).
 */
function onChatListKeyDown(event: React.KeyboardEvent<HTMLElement>) {
  const target = event.target as HTMLElement;
  if (!target.matches("[data-chat-row]")) return;
  const rows = Array.from(event.currentTarget.querySelectorAll<HTMLElement>("[data-chat-row]"));
  const index = rows.indexOf(target);
  if (index === -1) return;
  let next: number;
  switch (event.key) {
    case "ArrowDown":
      next = Math.min(rows.length - 1, index + 1);
      break;
    case "ArrowUp":
      next = Math.max(0, index - 1);
      break;
    case "Home":
      next = 0;
      break;
    case "End":
      next = rows.length - 1;
      break;
    default:
      return;
  }
  event.preventDefault();
  rows[next]?.focus();
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

/**
 * A row's tooltip: its full title, and when it last changed. The row has no
 * time column (restyle §11.4); the time is here.
 */
function rowTooltip(title: string, chat: Chat): string {
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
  /** The one row in the tab order (the open chat, else the first). */
  isTabStop?: boolean;
  onChatClick: (chat: ChatWithActivity) => void;
  onContextMenu: (e: React.MouseEvent, chat: ChatWithActivity) => void;
  onEditingTitleChange: (title: string) => void;
  onSaveRename: (chatId: string) => void;
  onCancelRename: () => void;
}

const ChatItem = memo(function ChatItem({
  chat,
  inGroup = false,
  editingChatId,
  editingTitle: propsEditingTitle,
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
      onContextMenu={(e) => onContextMenu(e, chat)}
      hoverActions={!isEditing && <ChatMoreButton title={chatTitle} onOpen={(e) => onContextMenu(e, chat)} />}
    />
  );
});

// --- Extracted ArchivedChatItem (module-level for stable identity + React.memo) ---

interface ArchivedChatItemProps {
  chat: ChatWithActivity;
  activeChatId: string | null;
  /** The one row in the tab order (the open chat, else the first). */
  isTabStop?: boolean;
  /** Under a group header (not the flat list): indented to line up with it. */
  inGroup?: boolean;
  /** The chat's project, when no header above the row names it. */
  projectName?: string;
  /** The chat's workspace, when no header above the row names it. */
  workspaceName?: string;
}

const ArchivedChatItem = memo(function ArchivedChatItem({
  chat,
  activeChatId,
  isTabStop = false,
  inGroup = false,
  projectName,
  workspaceName,
}: ArchivedChatItemProps) {
  const chatId = chat.id;
  const chatTitle = chat.title || UNTITLED_CHAT;
  const isActive = activeChatId === chatId;
  const deleteChatMutation = usePermanentlyDeleteChat();
  const unarchiveMutation = useUnarchiveChat();
  const [confirmingDelete, setConfirmingDelete] = useState(false);

  const handleViewChat = () => {
    // The chat may be in another project: openChat moves the context there.
    openChat(chat).catch((error) => void toast.error(error));
  };

  const handleRestore = () => {
    unarchiveMutation.mutate(chatId);
  };

  // Deleting an archived chat is permanent, so it asks first — it used to be
  // one click on a hover icon, with no confirmation and no undo.
  const handleDelete = async () => {
    try {
      await deleteChatMutation.mutateAsync(chatId);
      setConfirmingDelete(false);
      toast.success(`Deleted "${chatTitle}"`);
    } catch (error) {
      setConfirmingDelete(false);
      void toast.error(error);
    }
  };

  const where =
    projectName || workspaceName ? (
      <>
        <span aria-hidden="true">· </span>
        {projectName && <span data-testid={`chat-project-${chatId}`}>{projectName}</span>}
        {projectName && workspaceName && " / "}
        {workspaceName}
      </>
    ) : undefined;

  return (
    <>
      <ChatRow
        chatId={chatId}
        title={chatTitle}
        tooltip={rowTooltip(chatTitle, chat)}
        current={isActive}
        inProject={inGroup}
        suffix={where}
        isTabStop={isTabStop}
        onClick={handleViewChat}
        hoverActions={
          <>
            <Tooltip content="Restore chat" side="left" delay={300}>
              <button
                type="button"
                aria-label={`Restore ${chatTitle}`}
                onClick={(e) => {
                  e.stopPropagation();
                  handleRestore();
                }}
                className={SIDEBAR_ICON_BUTTON}
              >
                <RotateCcw className="size-3.5" aria-hidden="true" />
              </button>
            </Tooltip>
            <Tooltip content="Delete permanently" side="left" delay={300}>
              <button
                type="button"
                aria-label={`Delete ${chatTitle} permanently`}
                aria-haspopup="dialog"
                onClick={(e) => {
                  e.stopPropagation();
                  setConfirmingDelete(true);
                }}
                className={cn(SIDEBAR_ICON_BUTTON, "hover:text-destructive-ink")}
              >
                <Trash2 className="size-3.5" aria-hidden="true" />
              </button>
            </Tooltip>
          </>
        }
      />

      {/* Portalled: the list scrolls. */}
      {confirmingDelete && createPortal(
      <ConfirmationDialog
        open={confirmingDelete}
        title={`Delete "${chatTitle}" permanently?`}
        description="The chat and all of its messages are deleted for good. This can't be undone. Restore it instead to keep it."
        confirmLabel="Delete permanently"
        loading={deleteChatMutation.isPending}
        onConfirm={() => void handleDelete()}
        onCancel={() => setConfirmingDelete(false)}
      />,
      document.body,
      )}
    </>
  );
});

// --- WorktreeGroupComponent ---

interface WorktreeGroupComponentProps {
  id: string;
  title: string;
  /** A quiet mark after the title (an archived workspace's glyph). */
  subtitle?: ReactNode;
  /** The header's mark when it is a top-level group (a workspace's branch, an archived project's mark). */
  icon: ReactNode;
  chats: ChatWithActivity[];
  isExpanded: boolean;
  onToggle: () => void;
  renderChat: (chat: ChatWithActivity) => ReactNode;
  onNewChat?: () => void;
  /**
   * What New chat says. A workspace's own by default; the heading of a
   * project's main checkout says the project's, since that one `+` is the
   * project's New chat too (one `+` per heading, owner 2026-11-25).
   */
  newChatLabel?: { tooltip: string; ariaLabel: string };
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
  /** Nested: the project the workspace is in, so its heading reads "project · branch" (U6d). */
  projectName?: string;
  /** Nested: what the heading keeps at rest (the project's pin, hide, needs-you marks). */
  headingTrail?: ReactNode;
  /** Nested: more hover actions on the heading (the project's own: New chat in it, its menu). */
  extraActions?: ReactNode;
  testId?: string;
}

const WorktreeGroupComponent = memo(function WorktreeGroupComponent({
  id: _id,
  title,
  subtitle,
  icon,
  chats,
  isExpanded,
  onToggle,
  renderChat,
  onNewChat,
  newChatLabel,
  onArchiveWorktree,
  onContextMenu,
  emptyState,
  nested = false,
  hideHeader = false,
  projectName,
  headingTrail,
  extraActions,
  testId,
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

  // New chat and Archive show on hover or focus, and take no width until
  // then: the branch keeps the row.
  const actions =
    onNewChat || onArchiveWorktree || extraActions ? (
      <>
        {onNewChat && (
          <Tooltip content={newChatLabel?.tooltip ?? "New chat in this workspace"} side="left" delay={300}>
            <button
              type="button"
              aria-label={newChatLabel?.ariaLabel ?? `New chat in ${title}`}
              onClick={(e) => {
                e.stopPropagation();
                onNewChat();
              }}
              className={SIDEBAR_ICON_BUTTON}
            >
              <Plus className="size-3.5" aria-hidden="true" />
            </button>
          </Tooltip>
        )}
        {onArchiveWorktree && (
          <Tooltip content="Archive this workspace" side="left" delay={300}>
            <button
              type="button"
              aria-label={`Archive ${title}`}
              onClick={(e) => {
                e.stopPropagation();
                onArchiveWorktree();
              }}
              className={SIDEBAR_ICON_BUTTON}
            >
              <Archive className="size-3.5" aria-hidden="true" />
            </button>
          </Tooltip>
        )}
        {extraActions}
      </>
    ) : undefined;

  return (
    <div className="space-y-0.5" data-testid={testId}>
      {nested ? (
        <ChatSectionHeading
          project={projectName}
          branch={title}
          expanded={isExpanded}
          onToggle={onToggle}
          trail={headingTrail}
          actions={actions}
          onContextMenu={onContextMenu}
          testId={testId ? `${testId}-heading` : undefined}
        />
      ) : (
        <ProjectGroupHeader
          name={title}
          mark={icon}
          expanded={isExpanded}
          onToggle={onToggle}
          trail={subtitle}
          actions={actions}
          onContextMenu={onContextMenu}
        />
      )}

      {isExpanded && chats.length > 0 && (
        <div className="space-y-0.5">
          {chats.map((chat) => (
            <div key={chat.id}>{renderChat(chat)}</div>
          ))}
        </div>
      )}

      {/* Empty state for workspace with no chats */}
      {isExpanded && chats.length === 0 && emptyState && (
        <button
          type="button"
          onClick={(e) => {
            e.stopPropagation();
            emptyState.onClick?.();
          }}
          className={cn(
            "flex h-7 w-full items-center rounded-md pl-8 text-left text-meta text-muted-foreground transition-colors",
            emptyState.onClick ? "cursor-pointer hover:bg-foreground/5 hover:text-foreground" : "cursor-default",
          )}
        >
          {emptyState.message}
        </button>
      )}
    </div>
  );
});

/**
 * Which of a project's workspace headings stands for the project itself (its
 * New chat is the project's): the main checkout, else the chats with no known
 * workspace (they run in the main checkout), else none (-1).
 */
function projectHomeIndex(workspaces: readonly ChatGroup[]): number {
  const main = workspaces.findIndex((w) => w.isMain && !w.worktreeId.startsWith("no-worktree"));
  return main !== -1 ? main : workspaces.findIndex((w) => w.worktreeId.startsWith("no-worktree"));
}

/** How many of one project's chats the Chats accordion lists before "All chats". */
const CHATS_CAP = 8;

/** The first `cap` chats, and `keepId` (the open chat) wherever it is. */
function capChats<T extends { id: string }>(chats: readonly T[], cap: number, keepId: string | null): T[] {
  const out = chats.slice(0, cap);
  const keep = keepId && !out.some((chat) => chat.id === keepId) ? chats.find((chat) => chat.id === keepId) : undefined;
  return keep ? [...out, keep] : out;
}

/** The groups' first `cap` chats in list order (and the open one), groups left empty dropped. */
function capChatGroups(groups: readonly ChatGroup[], cap: number, keepId: string | null): ChatGroup[] {
  let left = cap;
  const out: ChatGroup[] = [];
  for (const group of groups) {
    const kept = group.chats.filter((chat) => {
      if (chat.id === keepId) return true;
      if (left <= 0) return false;
      left -= 1;
      return true;
    });
    if (kept.length > 0) out.push({ ...group, chats: kept });
  }
  return out;
}

export function ChatShellNav({
  pathname = "",
  search = NO_SEARCH,
  onNavigate,
  onOpenRuns,
  config,
  hostSlots,
  chatSetup = null,
  find,
  scope,
}: ChatShellNavProps) {
  const go = useCallback(
    (href: string, options?: NavigateOptions) => (options ? onNavigate?.(href, options) : onNavigate?.(href)),
    [onNavigate],
  );
  // What the list shows is the project shell's scope (U6b, sidebar/shellProject.ts),
  // the one project filter (the switcher at the top of the nav): one project's
  // chats, several projects' (`?projects=`, hooks/useChatProjectFilter), or
  // every project's — grouped by project unless it is one. The Chats heading
  // has no project filter of its own (owner: one project filter, the global one).
  const routeShell = useShellProject(scope?.pathname ?? pathname, search);
  const adjustScope = scope?.adjust;
  const shell = useMemo(() => (adjustScope ? adjustScope(routeShell) : routeShell), [adjustScope, routeShell]);
  // Every other reader of the scope (the chat list load, the keyboard, a new chat) takes what this shows.
  usePublishShellScope(shell, search);
  // The bridges of the scope's choice of several (URL, remembered choice, org), run once, here.
  useChatProjectFilter({ sync: { pathname, search } });
  const projectFilterId = shell.mode === "one" ? shell.projectId : null;
  const severalProjects = shell.mode === "many" ? shell.ids : null;
  // One app with no code here has no chats to list.
  const noChatsHere = shell.mode === "one" && !shell.projectId && shell.key !== null;
  const { projects } = useProjects();
  const {
    data: chatList,
    isPending: chatListPending,
    isError: chatListFailed,
    refetch: refetchChatList,
  } = useChatListWithWorkspaces(severalProjects ?? projectFilterId, { enabled: !noChatsHere });
  const newChatShortcut = useShortcutLabel("newChat");
  const listedChats = useMemo(() => chatList?.chats ?? [], [chatList?.chats]);
  // The list carries each chat's workspace and every listed project's main
  // one, so the sidebar groups from this response alone.
  const mainWorktrees = chatList?.mainWorktrees;
  const scopedProjects = useMemo(
    () =>
      projectFilterId
        ? projects.filter((p) => p.id === projectFilterId)
        : severalProjects
          ? projects.filter((p) => severalProjects.includes(p.id))
          : projects,
    [projects, projectFilterId, severalProjects],
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
  // The machine filter (null: All machines) narrows the same list further. It
  // is read through effectiveMachineFilter: a machine the user no longer has
  // drops out of it, and with one machine there is nothing to filter.
  const machineFilterIds = useWorkspaceStateStore((state) => state.machineFilterIds);
  const setMachineFilterIds = useWorkspaceStateStore((state) => state.setMachineFilterIds);
  const { data: machines } = useDaemonList();
  // Branch workspaces' machines, for a chat not pinned yet; never fetched here.
  const cachedWorkspaces = useCachedWorktrees(ALL_PROJECTS);
  const machineContext = useMemo(
    () => machineResolver(machines ?? [], cachedWorkspaces),
    [machines, cachedWorkspaces],
  );
  const machineFilter = useMemo(
    () => effectiveMachineFilter(machineFilterIds, machines ?? []),
    [machineFilterIds, machines],
  );
  // Which machine holds which project, read only while the filter is on: it
  // narrows the project sections as well as the chats.
  const { data: projectCheckouts } = useProjectDaemonInstalls(!!machineFilter);
  // The menu offers the active org's machines (with several orgs), plus any
  // the filter already names; the chats it filters are the active org's.
  const machineOrgs = useMachineOrgs();
  const menuMachines = useMemo(
    () => machineOrgs.inActiveOrg(machines ?? [], machineFilter ?? []),
    [machineOrgs, machines, machineFilter],
  );
  const showMachineFilter = menuMachines.length > 1;
  const handleMachineFilterChange = useCallback(
    (filter: MachineFilter) => setMachineFilterIds(filter),
    [setMachineFilterIds],
  );
  // Which project sections All projects lists: the active ones and the
  // pinned ones, the rest under "More projects", hidden ones left out; or,
  // with show-all, every one. Per person and per org (the user setting
  // sidebar.projects). The project the main pane works in always shows.
  const {
    prefs: projectPrefs,
    setPinned: setProjectPinned,
    setHidden: setProjectHidden,
    setShowAll: setShowAllProjects,
    // Changes wait for the choices to be read, so one never writes over them.
    writable: projectPrefsWritable,
    loadFailed: projectPrefsLoadFailed,
    retry: retryProjectPrefs,
  } = useSidebarProjectPrefs();
  const currentProjectId = useProjectStore((state) => state.currentProject?.id);
  const [moreProjectsOpen, setMoreProjectsOpen] = useState(false);
  const releasedChatId = useSidebarPinStore((state) => state.releasedChatId);
  const forgetReleasedUnless = useSidebarPinStore((state) => state.forgetUnless);
  useEffect(() => {
    forgetReleasedUnless(activeChatId);
  }, [activeChatId, forgetReleasedUnless]);
  // Another member's shared chat is never pinned into the person's own list:
  // its row is in "Shared with <org>" (SharedWithOrgSection), and pinned here
  // it would sit under a project and workspace that are not theirs.
  const pinnable = openedChat?.id === activeChatId && !openedChat?.viewerAccess ? openedChat : undefined;
  const chats = useMemo(
    () =>
      withOpenChatPinned(listedChats, pinnable, {
        scope: projectFilterId,
        releasedChatId,
      }),
    [listedChats, pinnable, projectFilterId, releasedChatId],
  );
  // The rows the list renders: a run counted here has its own row, so the
  // automation pill does not count it again.
  const listedChatIds = useMemo(() => new Set(chats.map((chat) => chat.id)), [chats]);
  // The Workflows row's live count is about the projects the nav shows.
  const liveRunsScope = useMemo(
    (): LiveRunsScope =>
      severalProjects
        ? { kind: "projects", projectIds: severalProjects }
        : shell.mode === "all"
          ? { kind: "all" }
          : projectFilterId
            ? { kind: "projects", projectIds: [projectFilterId] }
            : { kind: "none" },
    [severalProjects, shell.mode, projectFilterId],
  );
  // Where each place is, in the form the links use (lib/paths), and whether
  // its page is the one on screen.
  // New chat starts in the shell's one project; across several, wherever openNewChat would.
  const newChatHref = withSearch(newChatPath(), { project: projectFilterId ?? undefined });
  const inboxHref = inboxPath();
  const [showAllChats, setShowAllChats] = useState(false);
  useEffect(() => setShowAllChats(false), [shell.key, shell.mode]);
  // Focus target for the "focus the chat list" shortcut. The list has no
  // single natural control to focus, so the pane container takes it and
  // ordinary tab-order takes over from there.
  const sidebarRootRef = useRef<HTMLDivElement>(null);
  // A phone: the nav is the shell's drawer (Layout/shell/AppShell).
  const narrow = useNarrowViewport();
  useEffect(() => {
    const handleFocus = () => sidebarRootRef.current?.focus();
    const name = focusEventName("left-sidebar");
    window.addEventListener(name, handleFocus);
    return () => window.removeEventListener(name, handleFocus);
  }, []);

  // Activity from activityStore (SINGLE SOURCE OF TRUTH)
  const archiveChatMutation = useArchiveChat();
  const renameChatMutation = useRenameChat();
  const markUnreadMutation = useMarkUnread();
  const unarchiveMutation = useUnarchiveChat();
  const activities = useActivityStore((state) => state.activities);
  const workingThreads = useActivityStore((state) => state.workingThreads);

  const adoptRunMutation = useAdoptRun();
  const unadoptRunMutation = useUnadoptRun();

  // Chat list preferences
  const sortOrder = useChatListPreferencesStore((state) => state.sortOrder);
  const viewMode = useChatListPreferencesStore((state) => state.viewMode);
  const setSortOrder = useChatListPreferencesStore((state) => state.setSortOrder);
  const setViewMode = useChatListPreferencesStore((state) => state.setViewMode);

  // UI State
  const [chatListTab, setChatListTab] = useState<"active" | "archived">("active");
  // Archived chats, newest first, a page at a time. Read only while the
  // Archived tab is open: closed, the list is unobserved, so the invalidations
  // every archive and every generated ChatService mutation send it only mark
  // it stale instead of refetching it.
  const archivedQuery = useArchivedChats({ enabled: chatListTab === "archived" });
  const archivedChats = archivedQuery.data ?? NO_ARCHIVED_CHATS;

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
  const [movingChatId, setMovingChatId] = useState<string | null>(null);
  // The chat, or the saved workflow behind one, whose share dialog is open.
  const [sharingChatId, setSharingChatId] = useState<string | null>(null);
  const [sharingWorkflow, setSharingWorkflow] = useState<ShareableWorkflow | null>(null);

  // Ref for scroll container
  const scrollContainerRef = useRef<HTMLDivElement>(null);

  // Archived chats in the filter's scope
  const projectArchivedChats = useMemo(() => {
    if (severalProjects) return archivedChats.filter((c) => severalProjects.includes(c.projectId));
    if (!projectFilterId) return archivedChats;
    return archivedChats.filter((c) => c.projectId === projectFilterId);
  }, [archivedChats, projectFilterId, severalProjects]);

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
  const {
    projectGroups,
    foldedProjectGroups,
    hiddenProjectCount,
    activeGroups,
    flatList,
    archivedChats: filteredArchivedChats,
    archivedGroups,
    hiddenByMachine,
  } = useMemo(() => {
    // ONLY active (non-archived) chats from the main list; a chat on a closed
    // workspace is not listed as active (chatsWithActivity already dropped the
    // archived ones).
    const allActiveWorktreeChats = activeWorkspaceChats(chatsWithActivity, mainWorktrees);
    const listing = { ...projectPrefs, currentProjectId, now: Date.now() };
    // Across projects, a hidden project's chats are left out with it, in the
    // flat view too (the navigation store filters the same way).
    const machineActiveChats = filterChatsByMachine(allActiveWorktreeChats, machineFilter, machineContext);
    // The scope again on the client: a list read before the scope moved, or a
    // server that ignores it, never shows another project's chats here.
    const activeWorktreeChats = projectFilterId
      ? machineActiveChats.filter((chat) => chat.projectId === projectFilterId)
      : severalProjects
        ? machineActiveChats.filter((chat) => severalProjects.includes(chat.projectId))
        : machineActiveChats.filter((chat) => !isProjectHiddenFromSidebar(chat.projectId, listing));
    const machineArchivedChats = filterChatsByMachine(projectArchivedChats, machineFilter, machineContext);

    // Convert archived chats from store to ChatWithActivity format
    const archivedList: ChatWithActivity[] = machineArchivedChats.map((chat) => ({
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
    // A project is a section only if the machine filter would list something
    // in it: a checkout on a selected machine, or a chat it lists.
    const machineProjects = filterProjectsByMachine(scopedProjects, machineFilter, projectCheckouts, activeWorktreeChats);
    const projectListing = projectFilterId
      ? { listed: [], folded: [], hiddenCount: 0 }
      : listProjectGroups(
          groupByProject(activeWorktreeChats, mainWorktrees, machineProjects, groupOptions),
          machineProjects,
          listing,
        );
    const projectGroups = projectListing.listed;
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
      foldedProjectGroups: projectListing.folded,
      hiddenProjectCount: projectListing.hiddenCount,
      activeGroups: groupsArray,
      flatList,
      archivedChats: sortChatList([...archivedList]),
      archivedGroups: archivedGroupsArray,
      hiddenByMachine: {
        active: allActiveWorktreeChats.length - machineActiveChats.length,
        archived: projectArchivedChats.length - machineArchivedChats.length,
      },
    };
  }, [
    projectPrefs,
    currentProjectId,
    machineFilter,
    machineContext,
    chatsWithActivity,
    projectArchivedChats,
    sortOrder,
    mainWorktrees,
    projectFilterId,
    severalProjects,
    scopedProjects,
    projectCheckouts,
    projectNames,
  ]);

  // The one chat row in the tab order: the open chat when the list shows it,
  // else the first row. Up/Down (onChatListKeyDown) move between rows; Tab
  // leaves the list rather than walking every chat.
  const tabStopChatId = useMemo(() => {
    const ordered =
      chatListTab === "archived"
        ? filteredArchivedChats
        : viewMode === "flat"
          ? flatList
          : (projectFilterId ? activeGroups : projectGroups.flatMap((group) => group.workspaces)).flatMap(
              (group) => group.chats,
            );
    if (activeChatId && ordered.some((chat) => chat.id === activeChatId)) return activeChatId;
    return ordered[0]?.id ?? null;
  }, [chatListTab, filteredArchivedChats, viewMode, flatList, projectFilterId, activeGroups, projectGroups, activeChatId]);

  // Whether chats can run yet (the host's ChatSetupGate). Until they can, the
  // Chats section is the Set up chats card, and New chat goes where the card does.
  const setupNeeded = chatSetup !== null;

  // New chat: in the shell's project, else the open chat's, else the last one
  // used (openNewChat).
  const handleNewChat = () => {
    if (chatSetup) {
      go(chatSetup.href);
      return;
    }
    void openNewChat(projectFilterId ? { projectId: projectFilterId } : undefined);
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

  // The same, per machine, so filtering to one never hides that another needs you.
  const needsYouByMachine = useMemo(
    () => countByMachine(chatsWithActivity, machineContext, chatNeedsAttention),
    [chatsWithActivity, machineContext],
  );
  // Offer "No machine" only while some listed chat runs without one.
  const hasNoMachineChats = useMemo(
    () => chatsWithActivity.some((chat) => chat.noMachine) || projectArchivedChats.some((chat) => chat.noMachine),
    [chatsWithActivity, projectArchivedChats],
  );

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

      const currentTitle = chat.title || UNTITLED_CHAT;

      // Update state immediately - React will batch these updates
      setEditingChatId(chatId);
      setEditingTitle(currentTitle);
    },
    [chats]
  );

  const handleSaveRename = useCallback(async (chatId: string) => {
    const chat = chats.find((c) => c.id === chatId);
    if (!chat) return;

    const currentTitle = chat.title || UNTITLED_CHAT;
    const newTitle = editingTitle.trim();

    if (newTitle && newTitle !== currentTitle) {
      try {
        await renameChatMutation.mutateAsync({ chatId, title: newTitle });
      } catch (error) {
        console.error("Failed to rename chat:", error);
        void toast.error(error);
      }
    }

    setEditingChatId(null);
    setEditingTitle("");
  }, [chats, editingTitle, renameChatMutation]);

  const handleCancelRename = useCallback(() => {
    setEditingChatId(null);
    setEditingTitle("");
  }, []);

  // The saved workflow the open chat menu's chat was started from, when its
  // owner can share it: read only while that menu is open.
  const menuChat = contextMenu?.type === "chat" ? chats.find((c) => c.id === contextMenu.chatId) : undefined;
  const menuChatWorkflow = useChatWorkflowShare(menuChat);

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
        label: "Move to project…",
        icon: <FolderInput className="w-4 h-4" />,
        onClick: () => setMovingChatId(chatId),
      },
    ];

    // The chat header's share toggle, from the row: its owner only.
    if (canShareChat(chat)) {
      menuItems.push({
        label: chat.visibility === ResourceVisibility.ORG ? "Stop sharing…" : "Share…",
        icon: <Users className="w-4 h-4" />,
        onClick: () => setSharingChatId(chatId),
      });
    }
    const chatWorkflow = menuChat?.id === chatId ? menuChatWorkflow : null;
    if (chatWorkflow) {
      menuItems.push({
        label: chatWorkflow.shared ? "Stop sharing workflow…" : "Share workflow…",
        icon: <WorkflowIcon className="w-4 h-4" />,
        onClick: () => setSharingWorkflow(chatWorkflow),
      });
    }

    menuItems.push(
      {
        label: "Copy Chat ID",
        icon: <Copy className="w-4 h-4" />,
        onClick: () => copyToClipboard(chatId),
      },
    );

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
        // Archive, as the row's own button does, with the same Undo. It was
        // labelled "Delete" in red while it archived; deleting for good is
        // only offered on an archived chat, behind a confirmation.
        items: [
          {
            label: "Archive",
            icon: <Archive className="w-4 h-4" />,
            onClick: () => void handleArchiveChat(chatId),
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
    try {
      await archiveChatMutation.mutateAsync(chatId);
    } catch (error) {
      void toast.error(error);
      return;
    }
    toast.notify("Chat archived", {
      action: {
        label: "Undo",
        onClick: () => {
          unarchiveMutation.mutate(chatId);
        },
      },
    });
  }, [archiveChatMutation, unarchiveMutation]);

  // Reveal the active chat (switch tab, expand its group, scroll it into view)
  // when the selection changes — e.g. via keyboard navigation.
  //
  // This runs at most once per (chat, view mode): revealing is a response to the
  // selection changing, not a state the sidebar continuously enforces. Re-running
  // it on tab/expansion state would undo the user's own clicks, and re-running it
  // on every chat-list refetch would yank the sidebar around mid-browse.
  const revealedChatKeyRef = useRef<string | null>(null);
  // The chat the Archived tab was last opened for, from its detail alone.
  const archivedTabOpenedForRef = useRef<string | null>(null);
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

    // The archived list is read only while its tab is open. An archived chat
    // opened from elsewhere (the Inbox, a link) is known archived from its own
    // detail: open the tab, once per chat, and reveal it when its page arrives.
    if (!chat && openedChat?.id === activeChatId && openedChat.state === ChatState.ARCHIVED) {
      if (archivedTabOpenedForRef.current !== activeChatId) {
        archivedTabOpenedForRef.current = activeChatId;
        setChatListTab("archived");
      }
      return;
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
  }, [activeChatId, viewMode, chatsWithActivity, projectArchivedChats, archivedGroups, openedChat]);


  const toggleWorktreeGroup = (worktreeId: string) => {
    setExpandedWorktreeGroups((prev) => ({
      ...prev,
      [worktreeId]: !prev[worktreeId],
    }));
  };


  // Memoized icon to avoid new JSX reference each render: a top-level
  // workspace group's mark (the list filtered to one project).
  const worktreeIcon = useMemo(
    () => <GitBranch className="size-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />,
    []
  );

  // `layout`: a workspace inside a project section is nested under it, and
  // the project's only workspace, its main one, has no header of its own.
  // `projectHome`: the heading stands for the project's main checkout, so its
  // one New chat is the project's (the project's controls add no second `+`).
  const renderWorktreeGroup = useCallback(
    (
      group: ChatGroup,
      layout: {
        nested?: boolean;
        hideHeader?: boolean;
        projectName?: string;
        projectHome?: boolean;
        headingTrail?: ReactNode;
        extraActions?: ReactNode;
        testId?: string;
      } = {},
    ) => {
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

      // Handlers for worktree actions. The main checkout's New chat names its
      // workspace, so it starts there even when the open chat is on a branch.
      // Chats with no known workspace stand for the main checkout only when
      // the project has no main workspace to name.
      const handleNewChat = !isNoWorktree
        ? () => {
            void handleSwitchToWorktreeNewChat(group.worktreeId, group.projectId);
          }
        : layout.projectHome
          ? () => {
              void openNewChat({ projectId: group.projectId });
            }
          : undefined;
      const newChatLabel =
        layout.projectHome && layout.projectName
          ? { tooltip: "New chat in this project", ariaLabel: `New chat in ${layout.projectName}` }
          : undefined;

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
          icon={worktreeIcon}
          nested={layout.nested}
          hideHeader={layout.hideHeader}
          projectName={layout.projectName}
          headingTrail={layout.headingTrail}
          extraActions={layout.extraActions}
          testId={layout.testId}
          chats={group.chats}
          isExpanded={isExpanded}
          onToggle={handleToggle}
          renderChat={(chat) => (
            <ChatItem
              chat={chat}
              inGroup={true}
              editingChatId={editingChatId}
              editingTitle={editingTitle}
              activeChatId={activeChatId}
              isTabStop={chat.id === tabStopChatId}
              onChatClick={handleChatClick}
              onContextMenu={handleChatContextMenu}
              onEditingTitleChange={setEditingTitle}
              onSaveRename={handleSaveRename}
              onCancelRename={handleCancelRename}
            />
          )}
          onNewChat={handleNewChat}
          newChatLabel={newChatLabel}
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
      tabStopChatId,
      editingChatId,
      editingTitle,
      worktreeIcon,
      handleChatClick,
      handleChatContextMenu,
      handleSaveRename,
      handleSwitchToWorktreeNewChat,
      handleCancelRename,
      handleWorktreeContextMenu,
    ]
  );

  // A project's sections in the All projects view (U6d, owner): one small
  // "project · branch" heading per workspace, every chat at the list's left
  // edge. The project's own controls (New chat in it, pin, hide) and marks
  // (pinned, hidden, needs you) sit on its first heading.
  const renderProjectSection = (group: (typeof projectGroups)[number]) => {
    const isPinned = projectPrefs.pinned.has(group.projectId);
    // Listed while hidden: under show-all, or as the current project.
    const isHidden = projectPrefs.hidden.has(group.projectId);
    // A chat here waits on the user: the header says so, folded or not.
    const needsYou = group.chats.some((chat) => chat.liveActivity === ChatActivity.AWAITING_INPUT);
    const projectActions: MenuGroup[] = [
      {
        items: [
          {
            label: isPinned ? "Unpin" : "Pin to sidebar",
            icon: isPinned ? <PinOff className="h-3.5 w-3.5" /> : <Pin className="h-3.5 w-3.5" />,
            onClick: () => setProjectPinned(group.projectId, !isPinned),
            disabled: !projectPrefsWritable,
            testId: `sidebar-project-pin-${group.projectId}`,
          },
          {
            label: isHidden ? "Unhide" : "Hide from sidebar",
            icon: isHidden ? <Eye className="h-3.5 w-3.5" /> : <EyeOff className="h-3.5 w-3.5" />,
            onClick: () => setProjectHidden(group.projectId, !isHidden),
            disabled: !projectPrefsWritable,
            testId: `sidebar-project-hide-${group.projectId}`,
          },
        ],
      },
    ];
    // At rest, in this order: pinned, hidden, needs you (restyle §12a).
    const trail =
      isPinned || isHidden || needsYou ? (
        <>
          {isPinned && (
            <span className="flex shrink-0" title="Pinned" data-testid={`sidebar-project-pinned-${group.projectId}`}>
              <Pin className="size-3 text-muted-foreground" aria-hidden="true" />
              <span className="sr-only">Pinned</span>
            </span>
          )}
          {isHidden && (
            <span
              className="flex shrink-0"
              title={projectPrefs.showAll ? "Hidden" : "Hidden, shown while you are in it"}
              data-testid={`sidebar-project-hidden-${group.projectId}`}
            >
              <EyeOff className="size-3 text-muted-foreground" aria-hidden="true" />
              <span className="sr-only">Hidden</span>
            </span>
          )}
          {needsYou && (
            <span
              role="img"
              aria-label="A chat needs you"
              title="A chat needs you"
              className="size-2 shrink-0 rounded-full bg-warning"
              data-testid={`sidebar-project-needs-you-${group.projectId}`}
            />
          )}
        </>
      ) : undefined;
    // The project's menu only: its New chat is the main checkout's heading's
    // one `+` (projectHome below), never a second `+` beside it (owner,
    // 2026-11-25: "reliant-labs · main" showed `+ + …`).
    const projectControls = (
      <>
        <DropdownMenu
          align="right"
          density="compact"
          label={`${group.projectName} actions`}
          menuClassName="min-w-[170px]"
          groups={projectActions}
          trigger={(props) => (
            <button
              type="button"
              {...props}
              aria-label={`${group.projectName} actions`}
              data-testid={`sidebar-project-actions-${group.projectId}`}
              className={SIDEBAR_ICON_BUTTON}
            >
              <MoreHorizontal className="size-4" aria-hidden="true" />
            </button>
          )}
        />
      </>
    );
    const homeIndex = projectHomeIndex(group.workspaces);
    return (
      <section key={group.projectId} aria-label={group.projectName} data-testid={`sidebar-project-${group.projectId}`} className="space-y-0.5">
        {group.workspaces.map((workspace, i) =>
          renderWorktreeGroup(workspace, {
            nested: true,
            projectName: group.projectName,
            projectHome: i === homeIndex,
            headingTrail: i === 0 ? trail : undefined,
            // The main workspace's own New chat is the project's; the project's menu rides its first heading.
            extraActions: i === 0 ? projectControls : undefined,
            testId: i === 0 ? `sidebar-project-header-${group.projectId}` : undefined,
          }),
        )}
        {group.chats.length === 0 && (
          <button
            type="button"
            onClick={() => void openNewChat({ projectId: group.projectId })}
            className="flex h-7 w-full items-center rounded-md pl-8 text-left text-meta text-muted-foreground transition-colors hover:bg-foreground/5 hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring"
          >
            No chats yet · start one
          </button>
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

      // Icon showing this workspace is archived
      const subtitle = group.isWorktreeArchived ? (
        <span className="flex shrink-0 text-muted-foreground" title="Workspace archived">
          <Archive className="size-3" aria-hidden="true" />
          <span className="sr-only">Workspace archived</span>
        </span>
      ) : undefined;

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
              <ProjectMark name={group.worktreeName} />
            ) : (
              <GitBranch className="size-3.5 shrink-0 text-muted-foreground" aria-hidden="true" />
            )
          }
          chats={group.chats}
          isExpanded={isExpanded}
          onToggle={handleToggle}
          renderChat={(chat) => (
            <ArchivedChatItem
              chat={chat}
              activeChatId={activeChatId}
              isTabStop={chat.id === tabStopChatId}
              inGroup
              workspaceName={isProject ? chat.worktreeName || undefined : undefined}
            />
          )}
          onContextMenu={handleWorktreeMenu}
        />
      );
    },
    [expandedArchivedGroups, activeChatId, tabStopChatId, handleWorktreeContextMenu]
  );

  const activeChatCount = activeGroups.reduce((total, group) => total + group.chats.length, 0);
  const visibleChatCount = chatListTab === "active" ? activeChatCount : filteredArchivedChats.length;
  const hasAnyVisibleChats = visibleChatCount > 0;
  // "No active chats" is only true once the list has answered: while it loads
  // the rows are skeletons, and a failure says so with a Retry, rather than
  // telling someone with 200 chats that they have none.
  const listLoading = chatListTab === "active" && chatListPending && !chatList && !noChatsHere;
  const listFailed = chatListTab === "active" && chatListFailed && !chatList;
  // Chats the machine filter keeps out of the current tab.
  const hiddenByMachineCount = chatListTab === "active" ? hiddenByMachine.active : hiddenByMachine.archived;
  const machineFilterName = machineFilter ? machineFilterLabel(machineFilter, machines ?? []) : "";
  const hiddenByMachineText = `${hiddenByMachineCount} ${hiddenByMachineCount === 1 ? "chat" : "chats"} on other machines`;
  const showAllOffered =
    !projectFilterId && !severalProjects && (projectPrefs.showAll || foldedProjectGroups.length > 0 || hiddenProjectCount > 0);

  // One project's chats are capped (the open one always shows), then "All
  // chats"; several projects' are grouped by project and scroll in the region.
  const capped = !!projectFilterId && !showAllChats && activeChatCount > CHATS_CAP;
  const shownGroups = capped ? capChatGroups(activeGroups, CHATS_CAP, activeChatId) : activeGroups;
  const shownFlat = capped ? capChats(flatList, CHATS_CAP, activeChatId) : flatList;
  const chatsNeedYou = [...needsYouByProject.values()].reduce((total, count) => total + count, 0);

  // The chat list's controls: the machine filter and the list's menu (sort, view, archived, show all).
  const chatListActions = !setupNeeded && !noChatsHere && (
    <>
      {showMachineFilter && (
        // At rest only on hover, like the menu; filtered, it names what it shows and stays.
        <span className={cn("flex min-w-0", !machineFilter && SIDEBAR_HEADING_HOVER_ACTION)}>
          <MachineFilterMenu
            filter={machineFilter}
            machines={menuMachines}
            onChange={handleMachineFilterChange}
            includeNoMachine={hasNoMachineChats || !!machineFilter?.includes(NO_MACHINE)}
            needsYouByMachine={needsYouByMachine}
          />
        </span>
      )}
      <span className={cn("flex", SIDEBAR_HEADING_HOVER_ACTION)}>
        <ChatListMenu
          filterId={projectFilterId}
          projects={projects}
          onAddProject={() => openAddProject()}
          onManageProjects={() => go(projectsPath())}
          sortOrder={sortOrder}
          onSortChange={setSortOrder}
          viewMode={viewMode}
          onViewChange={setViewMode}
          showAll={
            showAllOffered
              ? { on: projectPrefs.showAll, onChange: setShowAllProjects, disabled: !projectPrefsWritable }
              : undefined
          }
          archived={chatListTab === "archived"}
          onArchivedChange={(archived) => setChatListTab(archived ? "archived" : "active")}
        />
      </span>
    </>
  );

  // What the nav config's slots are (Layout/sidebar/navConfig): the live pieces only the chat shell can draw.
  const slots: NavSlots = {
    "new-chat": ({ depth }) => (
      // While chats cannot run, New chat sets them up first (handleNewChat).
      <SidebarSubRow
        label="New chat"
        ariaLabel="New chat"
        depth={depth}
        icon={<SquarePen className="size-4" />}
        href={chatSetup ? chatSetup.href : newChatHref}
        onClick={handleNewChat}
        trail={!setupNeeded && newChatShortcut ? <kbd className="font-sans">{newChatShortcut}</kbd> : undefined}
        testId="create-chat-button"
      />
    ),
    "chat-list": ({ place, depth }) => (
      <div
        id="sidebar-chats"
        data-testid="sidebar-chats"
        data-place={place}
        className={place === "fill" ? "flex min-h-0 flex-1 flex-col" : undefined}
      >
        {/* "Chats" over the list, its controls at the right on hover. */}
        <div
          className={cn("group/heading flex h-7 shrink-0 items-center gap-1 pr-1 text-meta font-medium text-muted-foreground", depth === 0 ? "mt-3 pl-2" : "mt-1 pl-8")}
          data-onboarding="project-filter"
          data-testid="sidebar-chats-heading"
        >
          {/* Which projects it lists is the switcher's (ProjectSwitcherRow), never a filter here. */}
          <span className="flex min-w-0 flex-1 items-center gap-1 truncate">Chats</span>
          {chatListActions}
        </div>
        {/* No agent sandbox can run a chat yet: connect one (your machine, or a Reliant sandbox), in place of the list. */}
        {chatSetup ? (
          chatSetup.card
        ) : (
          // The one element that bounds the chats: the rest of a drill-in's column, or 45% of the window in an accordion.
          <div
            className={cn("overflow-y-auto overscroll-contain", place === "fill" ? "min-h-0 flex-1" : "max-h-[45dvh]")}
            data-testid="sidebar-scroller"
          >
                  {chatListTab === "archived" && (
                    <div className="flex h-7 items-center gap-2 pl-8 pr-2 text-meta text-muted-foreground">
                      <span className="min-w-0 flex-1 truncate">Archived</span>
                      <button
                        type="button"
                        onClick={() => setChatListTab("active")}
                        data-testid="chat-list-back"
                        className="shrink-0 rounded-md px-1 text-primary-ink hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                      >
                        Back
                      </button>
                    </div>
                  )}
                  <div
                    ref={scrollContainerRef}
                    className="pb-1 pt-0.5"
                    onKeyDown={onChatListKeyDown}
                    aria-label={chatListTab === "active" ? "Chats" : "Archived chats"}
                    role="region"
                  >
                    {/* The pins and hides did not load: one quiet line. */}
                    {chatListTab === "active" && !projectFilterId && projectPrefsLoadFailed && (
                      <div className="mb-1 flex items-center justify-between gap-2 pl-8 pr-2 text-meta text-muted-foreground" data-testid="project-prefs-load-failed">
                        <span className="min-w-0 truncate">Couldn't load pinned and hidden projects</span>
                        <button
                          type="button"
                          onClick={retryProjectPrefs}
                          className="shrink-0 rounded font-medium text-primary-ink hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                        >
                          Retry
                        </button>
                      </div>
                    )}
                    {noChatsHere ? (
                      <p className="py-1 pl-14 pr-2 text-meta text-muted-foreground" data-testid="sidebar-chats-app-only">
                        This app has no code here, so no chats.
                      </p>
                    ) : chatListTab === "active" ? (
                      viewMode === "grouped" ? (
                        projectFilterId ? (
                          shownGroups.length > 0 && (
                            <div className="space-y-0.5">
                              {shownGroups.map((group) =>
                                renderWorktreeGroup(group, {
                                  nested: true,
                                  projectName: projectNameOf(group.projectId),
                                  projectHome: group === shownGroups[projectHomeIndex(shownGroups)],
                                }),
                              )}
                            </div>
                          )
                        ) : (
                          (projectGroups.length > 0 || foldedProjectGroups.length > 0) && (
                            <div className="space-y-2">
                              {projectGroups.map((group) => renderProjectSection(group))}
                              {/* Projects with no chats, unused for a while and unpinned. */}
                              {foldedProjectGroups.length > 0 && (
                                <div data-testid="sidebar-more-projects" className="space-y-2">
                                  <SidebarSubRow
                                    label={`More projects (${foldedProjectGroups.length})`}
                                    depth={1}
                                    onClick={() => setMoreProjectsOpen((open) => !open)}
                                    expanded={moreProjectsOpen}
                                    testId="sidebar-more-projects-toggle"
                                  />
                                  {moreProjectsOpen && (
                                    <div className="space-y-2">{foldedProjectGroups.map((group) => renderProjectSection(group))}</div>
                                  )}
                                </div>
                              )}
                            </div>
                          )
                        )
                      ) : (
                        shownFlat.length > 0 && (
                          <div className="space-y-0.5">
                            {shownFlat.map((chat) => {
                              const worktree = getWorktreeForChat(chat);
                              // Across projects, the row names its project first; the
                              // branch only when it is not the project's main checkout.
                              const projectName = projectFilterId ? undefined : projectNameOf(chat.projectId);
                              const branch = projectFilterId || !worktree?.isMain ? worktree?.branch : undefined;
                              return (
                                <ChatItem
                                  key={chat.id}
                                  chat={chat}
                                  inGroup
                                  editingChatId={editingChatId}
                                  editingTitle={editingTitle}
                                  projectName={projectName}
                                  workspaceBranch={branch || undefined}
                                  activeChatId={activeChatId}
                                  isTabStop={chat.id === tabStopChatId}
                                  onChatClick={handleChatClick}
                                  onContextMenu={handleChatContextMenu}
                                  onEditingTitleChange={setEditingTitle}
                                  onSaveRename={handleSaveRename}
                                  onCancelRename={handleCancelRename}
                                />
                              );
                            })}
                          </div>
                        )
                      )
                    ) : filteredArchivedChats.length > 0 ? (
                      viewMode === "grouped" ? (
                        <div className="space-y-2">{archivedGroups.map((group) => renderArchivedGroup(group))}</div>
                      ) : (
                        <div className="space-y-0.5">
                          {filteredArchivedChats.map((chat) => (
                            <ArchivedChatItem
                              key={chat.id}
                              chat={chat}
                              activeChatId={activeChatId}
                              isTabStop={chat.id === tabStopChatId}
                              inGroup
                              projectName={projectFilterId ? undefined : projectNameOf(chat.projectId)}
                            />
                          ))}
                        </div>
                      )
                    ) : null}

                    {/* One project's chats past the cap: the rest in place. */}
                    {chatListTab === "active" && projectFilterId && activeChatCount > CHATS_CAP && (
                      <SidebarSubRow
                        label={showAllChats ? "Show fewer" : `All chats (${activeChatCount})`}
                        depth={2}
                        onClick={() => setShowAllChats((all: boolean) => !all)}
                        expanded={showAllChats}
                        testId="sidebar-all-chats"
                      />
                    )}

                    {/* Other members' shared chats, read-only; nothing when there are none. On both tabs:
                        the shared list is not filtered by the viewer's archive. */}
                    {!noChatsHere && <SharedWithOrgSection activeChatId={activeChatId ?? null} />}

                    {listLoading && (
                      <div role="status" aria-label="Loading chats" className="space-y-1.5 py-2 pl-8 pr-1" data-testid="chat-list-loading">
                        {Array.from({ length: 4 }, (_, i) => (
                          <div key={i} className="h-7 animate-pulse rounded-md bg-foreground/5 motion-reduce:animate-none" />
                        ))}
                      </div>
                    )}

                    {listFailed && (
                      <div role="alert" className="my-2 ml-8 mr-1 rounded-md border border-border px-3 py-2.5 text-meta">
                        <p className="text-ui font-medium text-destructive-ink">Couldn't load your chats</p>
                        <p className="mt-0.5 text-muted-foreground">Your chats are safe; the list didn't arrive.</p>
                        <Button className="mt-2" variant="outline" size="sm" onClick={() => void refetchChatList()}>
                          Retry
                        </Button>
                      </div>
                    )}

                    {/* The machine filter hides some or all of them: say so, and undo it. */}
                    {machineFilter && hiddenByMachineCount > 0 && (
                      <div
                        className="mt-1 flex items-center justify-between gap-2 pl-14 pr-2 text-meta text-muted-foreground"
                        data-testid={hasAnyVisibleChats ? "machine-filter-hidden" : "machine-filter-empty"}
                      >
                        <span className="min-w-0 truncate">
                          {hasAnyVisibleChats ? `${hiddenByMachineText} hidden` : `None on ${machineFilterName}`}
                        </span>
                        <button
                          type="button"
                          onClick={() => setMachineFilterIds(null)}
                          className="shrink-0 rounded font-medium text-primary-ink hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                        >
                          Show all
                        </button>
                      </div>
                    )}

                    {/* Empty: one quiet line under New chat. */}
                    {!hasAnyVisibleChats && !listLoading && !listFailed && !noChatsHere && !(machineFilter && hiddenByMachineCount > 0) && (
                      <p className="py-1 pl-14 pr-2 text-meta text-muted-foreground" data-testid="sidebar-chats-empty">
                        {chatListTab === "active" ? (shell.mode === "one" && shell.name ? `No chats in ${shell.name} yet` : "No chats yet") : "No archived chats"}
                      </p>
                    )}
                  </div>
          </div>
        )}
      </div>
    ),
    "workflows-pill": () => <AutomationActivityPill listedChatIds={listedChatIds} onOpenRuns={onOpenRuns} scope={liveRunsScope} />,
    "chats-need-you": () =>
      chatsNeedYou > 0 ? (
        <span role="img" aria-label="A chat needs you" title="A chat needs you" className="size-2 rounded-full bg-warning" />
      ) : null,
    inbox: () => (
      <InboxNavItem
        href={inboxHref}
        onOpen={() => go(inboxHref)}
        current={isAtOrBelow(pathname, inboxHref)}
      />
    ),
    // The host's own (the app's Help and update button, the demo's phone actions).
    ...hostSlots,
  };

  return (
    <ShellNavColumn
      ref={sidebarRootRef}
      config={config}
      slots={slots}
      shell={shell}
      pathname={pathname}
      search={search}
      onNavigate={go}
      flat={narrow}
      find={find}
    >
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
      {sharingChatId && <ShareChatDialog chatId={sharingChatId} open onClose={() => setSharingChatId(null)} />}
      <ShareWorkflowDialog workflow={sharingWorkflow} onClose={() => setSharingWorkflow(null)} />
      {movingChatId && (
        <MoveChatToProjectDialog chatId={movingChatId} open onClose={() => setMovingChatId(null)} />
      )}
      {/* Render complete */}
    </ShellNavColumn>
  );
}
