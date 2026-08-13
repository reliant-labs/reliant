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
 */

import { useCallback, useMemo, useState } from "react";
import { Link, useNavigate } from "@tanstack/react-router";
import {
  AlertCircle,
  Archive,
  ChevronDown,
  ChevronRight,
  GitBranch,
  Loader2,
  MessageSquarePlus,
  Plus,
} from "lucide-react";
import { GroupedVirtuoso } from "react-virtuoso";
import { useQueryClient } from "@tanstack/react-query";
import { useChatList, chatKeys } from "../../hooks/chat-queries";
import { useProjectStore } from "../../store/projectStore";
import { useWorktreeStore } from "../../store/worktreeStore";
import { cn } from "../../lib/utils";
import { isChatBusy, needsUserAttention } from "../../lib/chatSendState";
import { relativeTime } from "./relativeTime";
import { MobileMenuButton } from "./MobileMenuButton";
import {
  MOBILE_PRIMARY_ACTION,
  MobileEmptyState,
  MobileScreenHeader,
} from "./MobileChrome";
import { ChatState } from "../../gen/reliant/v1/chat_pb";
// The DOMAIN Chat (types/chat), not the raw protobuf one: it is
// `Omit<ProtoChat, '$typeName'>` plus the client-side fields the API layer
// flattens on (worktreeName / worktreeDeletedAt), and it is what
// api.chatsV2.list — and therefore useChatList — actually returns. Importing
// the pb type here made every list value fail to assign for want of a
// `$typeName` this component never reads.
import type { Chat } from "../../types/chat";
import type { Worktree } from "../../store/worktreeStore";

interface ChatGroup {
  worktreeId: string;
  worktreeName: string;
  worktreeBranch: string;
  isMain: boolean;
  chats: Chat[];
  hasActivity: boolean;
  lastActivityAt: number;
}

function sortChatsWithinGroup(chats: Chat[]): Chat[] {
  return [...chats].sort((a, b) => {
    const aAttention = needsUserAttention({
      activity: a.activity,
      needsRecovery: a.needsRecovery,
    });
    const bAttention = needsUserAttention({
      activity: b.activity,
      needsRecovery: b.needsRecovery,
    });
    if (aAttention !== bAttention) return aAttention ? -1 : 1;

    const aTime = a.lastMessageAt ? Date.parse(a.lastMessageAt) : 0;
    const bTime = b.lastMessageAt ? Date.parse(b.lastMessageAt) : 0;
    return bTime - aTime;
  });
}

function buildGroups(chats: Chat[], worktrees: Worktree[]): ChatGroup[] {
  const worktreesById = new Map(worktrees.map((w) => [w.id, w]));
  const groupsById = new Map<string, ChatGroup>();

  for (const chat of chats) {
    if (chat.state === ChatState.ARCHIVED) continue;
    const worktree = chat.worktreeId ? worktreesById.get(chat.worktreeId) : undefined;
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
        worktreeName: worktree?.name ?? "Unknown workspace",
        worktreeBranch: worktree?.branch ?? "",
        isMain: worktree?.is_main ?? false,
        chats: [chat],
        hasActivity: false,
        lastActivityAt: 0,
      });
    }
  }

  // Main worktree is always shown, even with zero chats, so there's always a
  // way to start a chat in the project's default workspace.
  const mainWorktree = worktrees.find((w) => w.is_main && !w.deleted_at);
  if (mainWorktree && !groupsById.has(mainWorktree.id)) {
    groupsById.set(mainWorktree.id, {
      worktreeId: mainWorktree.id,
      worktreeName: mainWorktree.name,
      worktreeBranch: mainWorktree.branch,
      isMain: true,
      chats: [],
      hasActivity: false,
      lastActivityAt: 0,
    });
  }

  const groups = Array.from(groupsById.values());
  for (const group of groups) {
    group.chats = sortChatsWithinGroup(group.chats);
    group.hasActivity = group.chats.some((chat) =>
      needsUserAttention({ activity: chat.activity, needsRecovery: chat.needsRecovery }),
    );
    group.lastActivityAt = group.chats.reduce((max, chat) => {
      const t = chat.lastMessageAt ? Date.parse(chat.lastMessageAt) : 0;
      return Math.max(max, t);
    }, 0);
  }

  groups.sort((a, b) => {
    if (a.isMain !== b.isMain) return a.isMain ? -1 : 1;
    if (a.hasActivity !== b.hasActivity) return a.hasActivity ? -1 : 1;
    return b.lastActivityAt - a.lastActivityAt;
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
function ChatRow({ chat, isLast }: { chat: Chat; isLast: boolean }) {
  const state = { activity: chat.activity, needsRecovery: chat.needsRecovery };
  const attention = needsUserAttention(state);
  const busy = isChatBusy(state);

  return (
    <div className="px-4">
      <Link
        to="/m/chats/$chatId"
        params={{ chatId: chat.id }}
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
          {chat.unread && !attention && (
            <span
              className="h-2 w-2 rounded-full bg-primary"
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
            {attention ? (
              <span className="flex items-center gap-1 rounded-full bg-destructive/10 px-1.5 py-0.5 font-medium text-destructive">
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
}

interface GroupHeaderProps {
  group: ChatGroup;
  isCollapsed: boolean;
  onToggle: () => void;
  onNewChat: () => void;
  onArchive: () => void;
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
function GroupHeader({ group, isCollapsed, onToggle, onNewChat, onArchive }: GroupHeaderProps) {
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
                ? "bg-destructive/10 text-destructive"
                : "bg-primary/10 text-primary",
            )}
          >
            {group.chats.length}
          </span>
        </button>

        <button
          type="button"
          onClick={onNewChat}
          aria-label={`New chat in ${group.worktreeName}`}
          className="flex min-h-[44px] min-w-[44px] shrink-0 items-center justify-center rounded-md text-muted-foreground active:bg-foreground/5"
        >
          <Plus className="h-4 w-4" />
        </button>

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
  const { data: chats, isLoading } = useChatList(currentProjectId);
  const worktrees = useWorktreeStore((s) => s.worktrees);
  const archiveWorktree = useWorktreeStore((s) => s.archiveWorktree);
  const queryClient = useQueryClient();

  const [collapsed, setCollapsed] = useState<Record<string, boolean>>({});
  const [confirmingArchive, setConfirmingArchive] = useState<ChatGroup | null>(null);

  const groups = useMemo(
    () => buildGroups(chats ?? [], worktrees),
    [chats, worktrees],
  );

  const toggleGroup = useCallback((worktreeId: string) => {
    setCollapsed((prev) => ({ ...prev, [worktreeId]: !prev[worktreeId] }));
  }, []);

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

  const handleArchiveConfirmed = useCallback(async () => {
    if (!confirmingArchive) return;
    await archiveWorktree(confirmingArchive.worktreeId);
    // Archiving a worktree also archives its chats server-side, but only the
    // worktree store refetches. Without invalidating the chat list the archived
    // chats stay in cache still pointing at a worktree that is gone, and
    // `buildGroups` drops them into its unknown-worktree fallback — leaving a
    // ghost "Unknown workspace" group until a manual reload.
    await queryClient.invalidateQueries({ queryKey: chatKeys.lists() });
    setConfirmingArchive(null);
  }, [confirmingArchive, archiveWorktree, queryClient]);

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
            <Plus className="h-5 w-5" />
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
          description="Start a chat to put an agent to work on this project."
          action={
            <Link to="/m/new" className={MOBILE_PRIMARY_ACTION}>
              <Plus className="h-4 w-4" />
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
                onNewChat={() =>
                  void navigate({
                    to: "/m/new",
                    search: { worktreeId: group.worktreeId },
                  })
                }
                onArchive={() => setConfirmingArchive(group)}
              />
            );
          }}
          itemContent={(index) => {
            const chat = flatChats[index];
            if (!chat) return null;
            return <ChatRow chat={chat} isLast={groupEndIndices.has(index)} />;
          }}
          components={{ Footer: () => <div className="h-8" /> }}
        />
      )}

      {confirmingArchive && (
        <div
          role="dialog"
          aria-modal="true"
          aria-label="Confirm archive"
          className="fixed inset-0 z-[9999] flex items-end justify-center bg-black/50"
          onClick={() => setConfirmingArchive(null)}
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
                className="flex min-h-[48px] flex-1 items-center justify-center rounded-lg border border-border text-sm font-medium text-foreground active:bg-muted"
              >
                Cancel
              </button>
              <button
                type="button"
                onClick={() => void handleArchiveConfirmed()}
                className="flex min-h-[48px] flex-1 items-center justify-center rounded-lg bg-destructive text-sm font-medium text-destructive-foreground active:opacity-80"
              >
                Archive
              </button>
            </div>
          </div>
        </div>
      )}
    </div>
  );
}
