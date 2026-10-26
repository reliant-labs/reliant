/**
 * Chats tab of `/m/search` — chat title search, tapping through to the chat.
 *
 * The desktop equivalent (`Layout/ChatSearch.tsx`, history mode) is a
 * keyboard-shortcut-only floating modal with a keyboard-nav footer ("↑↓
 * Navigate", "Tab Switch mode") that has no meaning on a touch device. This
 * reimplements only the query (useChatSearch: the same debounced server-side
 * FTS as desktop, with a client-side fallback when no project is selected)
 * and a tap-through `Link` instead of an `onClick` + imperative navigate —
 * desktop's `openChat` also does worktree-switching
 * side effects this list intentionally skips, since mobile has no worktree
 * picker to switch (`worktreeManage`/`projectSwitching` are both off here).
 */

import { Link } from "@tanstack/react-router";
import { Loader2, MessageSquare, SearchX } from "lucide-react";
import { useWorkspaceStateStore } from "../../store/workspaceStateStore";
import { useChatSearch } from "../../hooks/chat-pull-queries";
import { relativeTime } from "./relativeTime";
import {
  MOBILE_PRIMARY_ACTION,
  MOBILE_ROW,
  MobileCardGroup,
  MobileEmptyState,
  MobileRowIcon,
  MobileScreenBody,
} from "./MobileChrome";

export function MobileChatSearchTab({ query }: { query: string }) {
  // The chat list's scope: the /m/projects filter, or every project.
  const scope = useWorkspaceStateStore((s) => s.projectFilterId);
  const { results, isLoading } = useChatSearch(scope, query);

  if (isLoading && results.length === 0) {
    return (
      <div className="flex flex-1 items-center justify-center py-10">
        <Loader2 className="h-5 w-5 animate-spin text-muted-foreground" />
      </div>
    );
  }

  if (results.length === 0) {
    return query.trim() ? (
      <MobileEmptyState
        icon={SearchX}
        title="No chats found"
        description={`Nothing matches "${query.trim()}". Try a shorter or different term.`}
      />
    ) : (
      <MobileEmptyState
        icon={MessageSquare}
        title="No recent chats"
        description="Chats you open will show up here for quick access."
        action={
          <Link to="/m/new" className={MOBILE_PRIMARY_ACTION}>
            Start a chat
          </Link>
        }
      />
    );
  }

  return (
    <MobileScreenBody>
      {/* Labelled even in the search case: with the query box scrolled under
          the thumb, the label is what tells you whether you are looking at
          matches or at the recents fallback. */}
      <MobileCardGroup label={query.trim() ? "Results" : "Recent"}>
        {results.map((result) => (
          <Link
            key={result.chatId}
            to="/m/chats/$chatId"
            params={{ chatId: result.chatId }}
            className={MOBILE_ROW}
          >
            <MobileRowIcon icon={MessageSquare} />
            <div className="min-w-0 flex-1">
              <p className="truncate text-sm font-medium text-foreground">
                {result.title || "Untitled chat"}
              </p>
              <p className="text-xs text-muted-foreground">
                {relativeTime(result.updatedAt)}
              </p>
            </div>
          </Link>
        ))}
      </MobileCardGroup>
    </MobileScreenBody>
  );
}
