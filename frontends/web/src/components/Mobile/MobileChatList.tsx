/**
 * `/m/chats` — the mobile chat list.
 *
 * Grouped by workspace (worktree), matching the desktop model verified in
 * `Layout/Sidebar.tsx`: Project → Worktree → Chats. A flat attention-sorted
 * list (the previous shape) collapses that hierarchy — on a project with a
 * few active branches, chats from unrelated workspaces interleave and there
 * is no way to tell which workspace a chat belongs to without opening it.
 *
 * Within each group, ordering is still attention-first then recency — see
 * `lib/chatSendState`, shared with the composer so the list and the chat
 * screen can never disagree about what a chat's state means. Groups
 * themselves sort main-first, then by whether any chat in them needs
 * attention, then by most recent activity.
 *
 * ## One affordance per action
 *
 * "New chat" is the compose button in the header, and nothing else on this
 * screen. Every group header used to carry a `+` too, and in the common case
 * (one project, only the main workspace) that put two identical `+` icons a
 * few pixels apart. Which workspace a chat starts in is now chosen on
 * `/m/new` (its Workspace row), defaulting to main, the way the compose
 * screens of most mobile apps pick a destination.
 *
 * "New workspace" is a labeled action under the groups, next to the thing it
 * creates. It is not a second header icon competing with compose.
 *
 * Each group's folder button opens that workspace's Files and Git, the same
 * `MobileWorkspaceSheet` a chat's header opens, so a workspace can be looked
 * at without starting or opening a chat in it.
 */

import { Suspense, lazy, memo, useCallback, useMemo, useState } from "react";
import { Link, useNavigate } from "@tanstack/react-router";
import {
  AlertCircle,
  Archive,
  ChevronDown,
  ChevronRight,
  FolderGit2,
  GitBranch,
  GitBranchPlus,
  Loader2,
  MessageCircleQuestion,
  MessageSquarePlus,
} from "lucide-react";
import { GroupedVirtuoso } from "react-virtuoso";
import { useQueryClient } from "@tanstack/react-query";
import {
  useChatListWithWorkspaces,
  chatKeys,
  getChatFromCache,
  seedChatDetail,
} from "../../hooks/chat-queries";
import { prefetchChatMessages } from "../../hooks/message-queries";
import { useProjectStore } from "../../store/projectStore";
import { useWorkspaceStateStore } from "../../store/workspaceStateStore";
import { useProjects } from "../../hooks/project-queries";
import { cn } from "../../lib/utils";
import { isChatBusy, needsUserAttention } from "../../lib/chatSendState";
import { chatListIndicators, stillRunningPhrase } from "../../lib/runStatus";
import { relativeTime } from "./relativeTime";
import { MobileMenuButton } from "./MobileMenuButton";
import {
  MOBILE_PRIMARY_ACTION,
  MobileEmptyState,
  MobileScreenHeader,
} from "./MobileChrome";
import { MobileCreateWorkspaceSheet } from "./MobileCreateWorkspaceSheet";

// Lazy: the sheet brings the file tree, git status and plan panels, and this
// list is the landing screen whose chunk gates first paint. The chat screen
// imports the same module, so warming that route (MobileNavigationFeedback)
// usually has it cached before anyone taps.
const MobileWorkspaceSheet = lazy(() =>
  import("./MobileWorkspaceSheet").then((m) => ({ default: m.MobileWorkspaceSheet })),
);

/** Stands in for the workspace sheet while its chunk loads, so the tap visibly lands. */
function WorkspaceSheetFallback() {
  return (
    <div className="fixed inset-0 z-[9999] flex items-center justify-center bg-background">
      <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
    </div>
  );
}
import { useCapability } from "../../lib/surfaceContext";
import { ChatState } from "../../gen/reliant/v1/chat_pb";
// The DOMAIN Chat (types/chat), not the raw protobuf one: it is
// `Omit<ProtoChat, '$typeName'>` plus the client-side fields the API layer
// flattens on (worktreeName / worktreeDeletedAt), and it is what
// listChats (hooks/chat-actions) — and therefore useChatListWithWorkspaces — actually returns. Importing
// the pb type here made every list value fail to assign for want of a
// `$typeName` this component never reads.
import type { Chat } from "../../types/chat";
import type { WorktreeSummary } from "../../gen/reliant/v1/worktree_pb";
import { workspaceOfChat } from "../../lib/chatWorkspace";
import { sortChats, compareChatGroups } from "../../lib/chatListOrder";
import { useSortOrder, type ChatSortOption } from "../../store/chatListPreferencesStore";
import { archiveWorktree } from "../../hooks/worktree-actions";

interface ChatGroup {
  worktreeId: string;
  /** The project the workspace belongs to. */
  projectId: string;
  worktreeName: string;
  worktreeBranch: string;
  isMain: boolean;
  /** False for the fallback group of chats whose worktree row is gone. */
  hasWorktree: boolean;
  chats: Chat[];
  hasActivity: boolean;
  lastActivityAt: number;
}

const chatNeedsAttention = (chat: Chat) =>
  needsUserAttention({ activity: chat.activity, needsRecovery: chat.needsRecovery });

// Grouped from the chat list alone: each chat carries its workspace and the
// list carries each project's main one, so no worktree list is involved.
//
// `scope` is the list's project filter (null: every project). Across projects
// a group's name carries its project ("app · main"), and a project's empty
// main workspace is not listed — that would be one empty group per project.
export function buildGroups(
  chats: Chat[],
  mainWorktrees: readonly WorktreeSummary[] | undefined,
  sortOrder: ChatSortOption,
  { scope, projectNames }: { scope: string | null; projectNames?: ReadonlyMap<string, string> } = { scope: null },
): ChatGroup[] {
  const groupsById = new Map<string, ChatGroup>();
  const named = (projectId: string, name: string) => {
    const projectName = scope === null ? projectNames?.get(projectId) : undefined;
    return projectName ? `${projectName} · ${name}` : name;
  };

  for (const chat of chats) {
    if (chat.state === ChatState.ARCHIVED) continue;
    const worktree = workspaceOfChat(chat, mainWorktrees);
    // A chat whose worktree was deleted out from under it (rather than
    // archived through the normal flow) still needs somewhere to render —
    // group it under its own id so it doesn't silently vanish from the list.
    const worktreeId = worktree?.id ?? chat.worktreeId ?? "unknown";
    const existing = groupsById.get(worktreeId);
    if (existing) {
      existing.chats.push(chat);
    } else {
      groupsById.set(worktreeId, {
        worktreeId,
        projectId: chat.projectId,
        worktreeName: named(chat.projectId, worktree?.name ?? "Unknown workspace"),
        worktreeBranch: worktree?.branch ?? "",
        isMain: worktree?.isMain ?? false,
        hasWorktree: !!worktree,
        chats: [chat],
        hasActivity: false,
        lastActivityAt: 0,
      });
    }
  }

  // In one project's list its main worktree is always shown, even with zero
  // chats, so there's always a way to start a chat in its default workspace.
  const mainWorktree = scope ? mainWorktrees?.find((w) => w.projectId === scope) : undefined;
  if (mainWorktree && !mainWorktree.deletedAt && !groupsById.has(mainWorktree.id)) {
    groupsById.set(mainWorktree.id, {
      worktreeId: mainWorktree.id,
      projectId: mainWorktree.projectId,
      worktreeName: mainWorktree.name,
      worktreeBranch: mainWorktree.branch,
      isMain: true,
      hasWorktree: true,
      chats: [],
      hasActivity: false,
      lastActivityAt: 0,
    });
  }

  const groups = Array.from(groupsById.values());
  for (const group of groups) {
    group.chats = sortChats(group.chats, sortOrder, chatNeedsAttention);
    group.hasActivity = group.chats.some(chatNeedsAttention);
    group.lastActivityAt = group.chats.reduce((max, chat) => {
      const t = chat.lastMessageAt ? Date.parse(chat.lastMessageAt) : 0;
      return Math.max(max, t);
    }, 0);
  }

  // Main workspace pins to the top; the rest rank by their leading chat under
  // the selected sort order, so a group only moves when its top chat does.
  groups.sort((a, b) => {
    if (a.isMain !== b.isMain) return a.isMain ? -1 : 1;
    if (a.chats.length === 0 && b.chats.length > 0) return 1;
    if (b.chats.length === 0 && a.chats.length > 0) return -1;
    if (a.chats.length === 0 && b.chats.length === 0) {
      return a.worktreeId.localeCompare(b.worktreeId);
    }
    return compareChatGroups(a.chats, b.chats, sortOrder, chatNeedsAttention);
  });

  return groups;
}

/**
 * One chat inside a workspace group.
 *
 * The group's card is assembled from separately-virtualized pieces (see
 * `GroupHeader`), so the rounding lives on the outer inset wrapper: the
 * header rounds its top, the last row of a group rounds its bottom, and the
 * rows in between stay square. `isLast` comes from the flattened index rather
 * than CSS because `last:` cannot see across Virtuoso's item boundaries.
 */
function primeChatOpen(chat: Chat): void {
  if (!getChatFromCache(chat.id)) seedChatDetail(chat);
  prefetchChatMessages(chat.id);
}

// Memoized: Virtuoso re-invokes `itemContent` for every visible row whenever
// the list re-renders, and the list re-renders on every chat-list cache write
// (an activity change, an unread flip, a new message's lastMessageAt on any
// chat). React Query keeps an unchanged chat's object identity across those
// writes, so only the row whose chat actually changed re-renders.
const ChatRow = memo(function ChatRow({ chat, isLast }: { chat: Chat; isLast: boolean }) {
  const state = { activity: chat.activity, needsRecovery: chat.needsRecovery };
  const attention = needsUserAttention(state);
  const busy = isChatBusy(state);
  // Needs-input is its own pill, distinct from a failure's "Needs you" and
  // from unread, and it does not hide the agents still running beside it.
  const { needsInput, status } = chatListIndicators({
    activity: chat.activity,
    workingThreads: chat.workingThreads,
    unread: chat.unread,
  });

  return (
    <div className="px-4">
      <Link
        to="/m/chats/$chatId"
        params={{ chatId: chat.id }}
        // The press lands well before the tap completes and the chat screen
        // mounts: start a cold chat's transcript read now, and home the row's
        // Chat so the screen has it on the first frame (without it, a send in
        // that window had no chat and started a new one).
        onPointerDown={() => primeChatOpen(chat)}
        // 64px min touch target — comfortably above the 44px floor, and it
        // gives two lines of text room to breathe.
        className={cn(
          "flex min-h-16 w-full items-center gap-3 border-b border-border py-3 pl-4 pr-4 elevation-1",
          "active:bg-foreground/5",
          isLast && "rounded-b-lg border-b-0",
        )}
      >
        {/* A fixed-width rail rather than a conditional dot: without it, a
            chat gaining an unread dot shifts its own title sideways while
            its neighbours stay put. */}
        <span className="flex w-2 shrink-0 justify-center">
          {chat.unread && (
            <span
              className="h-2 w-2 rounded-full bg-primary"
              role="img"
              aria-label="Unread"
            />
          )}
        </span>
        <div className="min-w-0 flex-1">
          <span
            className={cn(
              "block truncate text-sm",
              chat.unread ? "font-semibold text-foreground" : "text-foreground",
            )}
          >
            {chat.title || "Untitled chat"}
          </span>

          <div className="mt-1 flex items-center gap-2 text-xs text-muted-foreground">
            {needsInput ? (
              <>
                <span
                  className="flex items-center gap-1 rounded-full bg-warning/10 px-1.5 py-0.5 font-medium text-warning-ink"
                  data-testid="mobile-chat-needs-input"
                >
                  <MessageCircleQuestion className="h-3 w-3" aria-hidden="true" />
                  Needs your input
                </span>
                {status && (
                  <span
                    className="flex items-center gap-1 rounded-full bg-primary/10 px-1.5 py-0.5 font-medium text-primary"
                    data-testid="mobile-chat-still-running"
                    aria-label={stillRunningPhrase(chat.workingThreads)}
                  >
                    <Loader2 className="h-3 w-3 animate-spin" aria-hidden="true" />
                    {chat.workingThreads} running
                  </span>
                )}
              </>
            ) : attention ? (
              <span className="flex items-center gap-1 rounded-full bg-destructive/10 px-1.5 py-0.5 font-medium text-destructive-ink">
                <AlertCircle className="h-3 w-3" />
                Needs you
              </span>
            ) : busy ? (
              <span className="flex items-center gap-1 rounded-full bg-primary/10 px-1.5 py-0.5 font-medium text-primary">
                <Loader2 className="h-3 w-3 animate-spin" />
                Working
              </span>
            ) : null}
            <span>{relativeTime(chat.lastMessageAt)}</span>
          </div>
        </div>

        <ChevronRight className="h-4 w-4 shrink-0 text-muted-foreground" />
      </Link>
    </div>
  );
});

interface GroupHeaderProps {
  group: ChatGroup;
  isCollapsed: boolean;
  onToggle: () => void;
  onArchive: () => void;
  /** Absent when the workspace can't be browsed (its worktree is gone). */
  onBrowse?: () => void;
}

/**
 * The workspace header that caps each group's card.
 *
 * Rounds its own top and, when collapsed or empty, its bottom too — a
 * collapsed group is a single standalone card, and only an expanded one hands
 * its bottom edge to the last `ChatRow`.
 *
 * The 24px top margin is what separates one group's card from the previous
 * one. It can't live on the scroller as a `space-y`, because Virtuoso renders
 * group headers and items as flat siblings.
 */
function GroupHeader({ group, isCollapsed, onToggle, onArchive, onBrowse }: GroupHeaderProps) {
  const capsBottom = isCollapsed || group.chats.length === 0;

  return (
    <div className="px-4 pt-6">
      <div
        className={cn(
          "flex min-h-[52px] w-full items-center gap-1 rounded-t-lg border-b border-border pl-2 pr-1 elevation-1",
          capsBottom && "rounded-b-lg border-b-0",
        )}
      >
        <button
          type="button"
          onClick={onToggle}
          aria-expanded={!isCollapsed}
          className="flex min-h-[44px] min-w-0 flex-1 items-center gap-2 rounded-md px-2 text-left active:bg-foreground/5"
        >
          {isCollapsed ? (
            <ChevronRight className="h-4 w-4 shrink-0 text-muted-foreground" />
          ) : (
            <ChevronDown className="h-4 w-4 shrink-0 text-muted-foreground" />
          )}
          <div className="min-w-0 flex-1">
            <div className="flex items-center gap-1.5">
              <span className="truncate text-base font-semibold text-foreground">
                {group.worktreeName}
              </span>
              {group.hasActivity && (
                <span
                  className="h-1.5 w-1.5 shrink-0 rounded-full bg-destructive"
                  aria-label="Needs attention"
                />
              )}
            </div>
            {group.worktreeBranch && (
              <div className="flex items-center gap-1 text-xs text-muted-foreground">
                <GitBranch className="h-2.5 w-2.5 shrink-0" />
                <span className="truncate">{group.worktreeBranch}</span>
              </div>
            )}
          </div>
          <span
            className={cn(
              "shrink-0 rounded-full px-2 py-0.5 text-xs font-medium",
              group.hasActivity
                ? "bg-destructive/10 text-destructive-ink"
                : "bg-primary/10 text-primary",
            )}
          >
            {group.chats.length}
          </span>
        </button>

        {/* Files and Git without opening a chat. Same glyph and same sheet as
            the chat header's "Open workspace", so it reads as the same place
            reached from somewhere else. */}
        {onBrowse && (
          <button
            type="button"
            onClick={onBrowse}
            aria-label={`Files and Git for ${group.worktreeName}`}
            className="flex min-h-[44px] min-w-[44px] shrink-0 items-center justify-center rounded-md text-muted-foreground active:bg-foreground/5"
          >
            <FolderGit2 className="h-4 w-4" />
          </button>
        )}

        {!group.isMain && (
          <button
            type="button"
            onClick={onArchive}
            aria-label={`Archive ${group.worktreeName}`}
            className="flex min-h-[44px] min-w-[44px] shrink-0 items-center justify-center rounded-md text-muted-foreground active:bg-foreground/5"
          >
            <Archive className="h-4 w-4" />
          </button>
        )}
      </div>
    </div>
  );
}

export function MobileChatList() {
  const navigate = useNavigate();
  const currentProjectId = useProjectStore((s) => s.currentProject?.id);
  const currentProjectName = useProjectStore((s) => s.currentProject?.name);
  // The list is the project filter's (/m/projects), every project by default.
  const scope = useWorkspaceStateStore((s) => s.projectFilterId);
  const { projects } = useProjects();
  const projectNames = useMemo(() => new Map(projects.map((p) => [p.id, p.name])), [projects]);
  const { data: chatList, isLoading } = useChatListWithWorkspaces(scope);
  const sortOrder = useSortOrder();
  const queryClient = useQueryClient();

  const [collapsed, setCollapsed] = useState<Record<string, boolean>>({});
  const [confirmingArchive, setConfirmingArchive] = useState<ChatGroup | null>(null);
  const [isArchiving, setIsArchiving] = useState(false);
  const [creatingWorkspace, setCreatingWorkspace] = useState(false);
  const [browsing, setBrowsing] = useState<ChatGroup | null>(null);
  const canCreateWorkspace = useCapability("worktreeCreate");

  const chats = chatList?.chats;
  const groups = useMemo(
    () => buildGroups(chats ?? [], chatList?.mainWorktrees, sortOrder, { scope, projectNames }),
    [chats, chatList?.mainWorktrees, sortOrder, scope, projectNames],
  );

  const toggleGroup = useCallback((worktreeId: string) => {
    setCollapsed((prev) => ({ ...prev, [worktreeId]: !prev[worktreeId] }));
  }, []);

  // Derived once per (groups, collapsed) change rather than on every render:
  // a fresh `groupCounts` array hands Virtuoso a new prop each render.
  const { totalChats, groupCounts, flatChats, groupEndIndices } = useMemo(() => {
    const totalChats = groups.reduce((sum, g) => sum + g.chats.length, 0);

    const groupCounts = groups.map((g) => (collapsed[g.worktreeId] ? 0 : g.chats.length));
    // Flatten visible (expanded) chats in group order, so a Virtuoso item index
    // maps directly to `flatChats[index]` regardless of which groups are
    // collapsed.
    const flatChats = groups.flatMap((g) => (collapsed[g.worktreeId] ? [] : g.chats));

    // Which flat indices end a group. `ChatRow` rounds its bottom corners there,
    // finishing the card the group header opened — CSS `last:` can't express
    // this because Virtuoso renders every row as a flat sibling of every other.
    const groupEndIndices = new Set<number>();
    groupCounts.reduce((offset, count) => {
      if (count > 0) groupEndIndices.add(offset + count - 1);
      return offset + count;
    }, 0);

    return { totalChats, groupCounts, flatChats, groupEndIndices };
  }, [groups, collapsed]);

  // Archiving can take as long as waking the workspace's machine (the store
  // retries across a wake for up to two minutes). Before this state existed the
  // sheet just sat there with a live Archive button, which read as a dead tap
  // and invited a second archive call.
  const handleArchiveConfirmed = useCallback(async () => {
    if (!confirmingArchive || isArchiving) return;
    setIsArchiving(true);
    try {
      // The store reports failure itself (toast) and does not throw.
      await archiveWorktree(confirmingArchive.worktreeId);
      // Archiving a worktree also archives its chats server-side, but only the
      // worktree store refetches. Without invalidating the chat list the
      // archived chats stay in cache still pointing at a worktree that is gone,
      // and `buildGroups` drops them into its unknown-worktree fallback —
      // leaving a ghost "Unknown workspace" group until a manual reload.
      await queryClient.invalidateQueries({ queryKey: chatKeys.lists() });
    } finally {
      setIsArchiving(false);
      setConfirmingArchive(null);
    }
  }, [confirmingArchive, isArchiving, queryClient]);

  // Memoized so Virtuoso gets a stable component type. An inline component
  // would remount the footer, and the button in it, on every render.
  const listComponents = useMemo(
    () => ({
      Footer: () =>
        canCreateWorkspace ? (
          <div className="px-4 pb-8 pt-6">
            <button
              type="button"
              onClick={() => setCreatingWorkspace(true)}
              className="flex min-h-[48px] w-full items-center justify-center gap-2 rounded-lg border border-dashed border-border text-sm font-medium text-primary active:bg-primary/10"
            >
              <GitBranchPlus className="h-4 w-4" />
              {/* Across projects, say which one it is made in. */}
              {scope === null && currentProjectName ? `New workspace in ${currentProjectName}` : "New workspace"}
            </button>
          </div>
        ) : (
          <div className="h-8" />
        ),
    }),
    [canCreateWorkspace, scope, currentProjectName],
  );

  return (
    <div className="flex h-full min-h-0 flex-col">
      <MobileScreenHeader
        title="Chats"
        leading={<MobileMenuButton />}
        trailing={
          <Link
            to="/m/new"
            className="flex min-h-[44px] min-w-[44px] items-center justify-center rounded-md text-muted-foreground active:bg-muted"
            aria-label="New chat"
          >
            {/* The same glyph the nav drawer uses for New chat, so the two
                entry points read as one action. */}
            <MessageSquarePlus className="h-5 w-5" />
          </Link>
        }
      />

      {isLoading ? (
        <div className="flex flex-1 items-center justify-center">
          <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
        </div>
      ) : totalChats === 0 && groups.length === 0 ? (
        <MobileEmptyState
          icon={MessageSquarePlus}
          title="No chats yet"
          description={
            scope ? "Start a chat to put an agent to work on this project." : "Start a chat to put an agent to work."
          }
          action={
            <Link to="/m/new" className={MOBILE_PRIMARY_ACTION}>
              <MessageSquarePlus className="h-4 w-4" />
              Start a chat
            </Link>
          }
        />
      ) : (
        <GroupedVirtuoso
          className="min-h-0 flex-1"
          groupCounts={groupCounts}
          computeItemKey={(index) => flatChats[index]?.id ?? index}
          groupContent={(groupIndex) => {
            const group = groups[groupIndex];
            if (!group) return null;
            return (
              <GroupHeader
                group={group}
                isCollapsed={collapsed[group.worktreeId] ?? false}
                onToggle={() => toggleGroup(group.worktreeId)}
                onArchive={() => setConfirmingArchive(group)}
                onBrowse={group.hasWorktree ? () => setBrowsing(group) : undefined}
              />
            );
          }}
          itemContent={(index) => {
            const chat = flatChats[index];
            if (!chat) return null;
            return <ChatRow chat={chat} isLast={groupEndIndices.has(index)} />;
          }}
          components={listComponents}
        />
      )}

      {browsing && (
        <Suspense fallback={<WorkspaceSheetFallback />}>
          <MobileWorkspaceSheet
            isOpen
            onClose={() => setBrowsing(null)}
            worktreeId={browsing.worktreeId}
            projectPath={projects.find((p) => p.id === browsing.projectId)?.path}
            title={browsing.worktreeName}
          />
        </Suspense>
      )}

      {creatingWorkspace && currentProjectId && (
        <MobileCreateWorkspaceSheet
          projectId={currentProjectId}
          onClose={() => setCreatingWorkspace(false)}
          onCreated={(worktree) => {
            setCreatingWorkspace(false);
            // A workspace is made to work in, and an empty one is not listed
            // here (groups come from chats). So land on the composer already
            // pointed at it, rather than back on a list that doesn't show it.
            void navigate({ to: "/m/new", search: { worktreeId: worktree.id } });
          }}
        />
      )}

      {confirmingArchive && (
        <div
          role="dialog"
          aria-modal="true"
          aria-label="Confirm archive"
          className="fixed inset-0 z-[9999] flex items-end justify-center bg-black/50"
          onClick={() => {
            if (!isArchiving) setConfirmingArchive(null);
          }}
        >
          <div
            className="w-full max-w-lg rounded-t-2xl border-t border-border bg-popover px-4 pt-5 shadow-2xl"
            style={{ paddingBottom: "calc(1.25rem + env(safe-area-inset-bottom))" }}
            onClick={(e) => e.stopPropagation()}
          >
            {/* A grab handle, so the sheet reads as a sheet rather than as a
                panel that appeared over the list. */}
            <div
              aria-hidden
              className="mx-auto mb-4 h-1 w-9 rounded-full bg-foreground/20"
            />
            <p className="mb-1.5 text-base font-semibold text-foreground">
              Archive {confirmingArchive.worktreeName}?
            </p>
            <p className="mb-5 text-sm text-muted-foreground">
              {confirmingArchive.chats.length > 0
                ? `${confirmingArchive.chats.length} chat${confirmingArchive.chats.length === 1 ? "" : "s"} in this workspace will be archived with it.`
                : "This workspace will be archived."}
            </p>
            <div className="flex gap-2">
              <button
                type="button"
                onClick={() => setConfirmingArchive(null)}
                disabled={isArchiving}
                className="flex min-h-[48px] flex-1 items-center justify-center rounded-lg border border-border text-sm font-medium text-foreground active:bg-muted disabled:opacity-60"
              >
                Cancel
              </button>
              <button
                type="button"
                onClick={() => void handleArchiveConfirmed()}
                disabled={isArchiving}
                className="flex min-h-[48px] flex-1 items-center justify-center gap-2 rounded-lg bg-destructive text-sm font-medium text-destructive-foreground active:opacity-80 disabled:opacity-60"
              >
                {isArchiving && <Loader2 className="h-4 w-4 animate-spin" />}
                {isArchiving ? "Archiving…" : "Archive"}
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
