/**
 * Chats tab of `/m/search` — chat title search, tapping through to the chat.
 *
 * The desktop equivalent (`Layout/ChatSearch.tsx`, history mode) is a
 * keyboard-shortcut-only floating modal with a keyboard-nav footer ("↑↓
 * Navigate", "Tab Switch mode") that has no meaning on a touch device. This
 * reimplements only the query: debounced call into `api.chatsV2.search`
 * (server-side FTS, same as desktop) with a client-side fallback when no
 * project is selected, and a tap-through `Link` instead of an `onClick` +
 * imperative navigate — desktop's `openChat` also does worktree-switching
 * side effects this list intentionally skips, since mobile has no worktree
 * picker to switch (`worktreeManage`/`projectSwitching` are both off here).
 */

import { useEffect, useRef, useState } from "react";
import { Link } from "@tanstack/react-router";
import { Loader2, MessageSquare, SearchX } from "lucide-react";
import { api } from "../../api/client";
import { useProjectStore } from "../../store/projectStore";
import { useChatList } from "../../hooks/chat-queries";
import { relativeTime } from "./relativeTime";
import {
  MOBILE_PRIMARY_ACTION,
  MOBILE_ROW,
  MobileCardGroup,
  MobileEmptyState,
  MobileRowIcon,
  MobileScreenBody,
} from "./MobileChrome";

interface ChatSearchResult {
  chatId: string;
  title: string;
  updatedAt: string;
}

export function MobileChatSearchTab({ query }: { query: string }) {
  const currentProject = useProjectStore((s) => s.currentProject);
  const { data: chats = [] } = useChatList(currentProject?.id);
  const [results, setResults] = useState<ChatSearchResult[]>([]);
  const [isLoading, setIsLoading] = useState(false);
  const debounceRef = useRef<ReturnType<typeof setTimeout> | null>(null);

  useEffect(() => {
    if (debounceRef.current) clearTimeout(debounceRef.current);

    const run = async () => {
      const trimmed = query.trim();
      if (!trimmed) {
        const recent = [...chats]
          .sort(
            (a, b) =>
              new Date(b.updatedAt).getTime() - new Date(a.updatedAt).getTime(),
          )
          .slice(0, 10)
          .map((c) => ({ chatId: c.id, title: c.title, updatedAt: c.updatedAt }));
        setResults(recent);
        return;
      }

      setIsLoading(true);
      try {
        if (currentProject) {
          const found = await api.chatsV2.search(currentProject.id, trimmed);
          setResults(
            found
              .slice(0, 15)
              .map((c) => ({ chatId: c.id, title: c.title, updatedAt: c.updatedAt })),
          );
        } else {
          const filtered = chats
            .filter((c) => c.title.toLowerCase().includes(trimmed.toLowerCase()))
            .slice(0, 15)
            .map((c) => ({ chatId: c.id, title: c.title, updatedAt: c.updatedAt }));
          setResults(filtered);
        }
      } catch (err) {
        console.error("Chat search failed:", err);
        const filtered = chats
          .filter((c) => c.title.toLowerCase().includes(trimmed.toLowerCase()))
          .slice(0, 15)
          .map((c) => ({ chatId: c.id, title: c.title, updatedAt: c.updatedAt }));
        setResults(filtered);
      } finally {
        setIsLoading(false);
      }
    };

    debounceRef.current = setTimeout(run, 300);
    return () => {
      if (debounceRef.current) clearTimeout(debounceRef.current);
    };
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [query, currentProject?.id]);

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
