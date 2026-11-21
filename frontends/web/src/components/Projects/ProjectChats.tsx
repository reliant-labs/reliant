// Copyright (c) 2026 Reliant Labs

/**
 * A project's chats on its landing page (U6c, Home and Overview merged):
 * the ones waiting on the person first, then the latest five, each to its
 * `/c/<id>`, and the way to all of them. The Overview of a deployed app
 * shows it in its rail; a project with no app shows it beside the
 * "Deploy this project" card.
 */

import { useMemo, type ReactNode } from "react";
import { useNavigate } from "@tanstack/react-router";
import { ChevronRight, MessageSquare } from "lucide-react";

import { useChatListWithWorkspaces } from "@/hooks/chat-queries";
import { chatPath, projectChatsPath } from "@/lib/paths";
import { formatRelativeTime } from "@/lib/relativeTime";
import { ChatActivity, useActivityStore } from "@/store/activityStore";

const RECENT = 5;

/** An in-app link: a plain click stays in the app, a modified one opens a tab. */
export function RowLink({ href, children, testId }: { href: string; children: ReactNode; testId?: string }) {
  const navigate = useNavigate();
  return (
    <a
      href={href}
      data-testid={testId}
      onClick={(event) => {
        if (event.metaKey || event.ctrlKey || event.shiftKey || event.altKey || event.button !== 0) return;
        event.preventDefault();
        void navigate({ href });
      }}
      className="no-color flex h-9 items-center gap-3 rounded-md px-2 text-ui text-ink transition-colors hover:bg-ink/5 focus:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring"
    >
      {children}
    </a>
  );
}

/** The project's chats, read once: every chat, the ones waiting on the person, and the latest few. */
export function useProjectChats(projectId: string | null) {
  const { data: list, isPending } = useChatListWithWorkspaces(projectId || null, { enabled: !!projectId });
  const activities = useActivityStore((state) => state.activities);
  const chats = useMemo(() => (list?.chats ?? []).filter((chat) => chat.projectId === projectId), [list?.chats, projectId]);
  const waiting = useMemo(() => chats.filter((chat) => activities.get(chat.id) === ChatActivity.AWAITING_INPUT), [chats, activities]);
  const recent = useMemo(
    () => [...chats].sort((a, b) => Date.parse(b.updatedAt || "") - Date.parse(a.updatedAt || "")).slice(0, RECENT),
    [chats]
  );
  return { chats, waiting, recent, loading: isPending && !list };
}

export function ProjectChats({ projectId, name }: { projectId: string; name: string }) {
  const { waiting, recent, loading } = useProjectChats(projectId);
  return (
    <section className="min-w-0 space-y-2" data-testid="project-landing-chats">
      <div className="flex min-h-7 items-center justify-between gap-3">
        <h2 className="text-section text-ink">
          Chats
          {waiting.length > 0 && <span className="ml-2 text-meta font-normal text-warning-ink">{waiting.length} waiting on you</span>}
        </h2>
      </div>
      {loading ? (
        <div className="space-y-1.5">
          {Array.from({ length: 3 }, (_, i) => (
            <div key={i} className="h-7 animate-pulse rounded-md bg-ink/5 motion-reduce:animate-none" />
          ))}
        </div>
      ) : recent.length === 0 && waiting.length === 0 ? (
        <p className="text-ui text-ink-muted">No chats in {name} yet.</p>
      ) : (
        <div className="-mx-2 space-y-0.5">
          {waiting.map((chat) => (
            <RowLink key={`w-${chat.id}`} href={chatPath(chat.id)} testId={`project-landing-waiting-${chat.id}`}>
              <span role="img" aria-label="Waiting on you" className="size-2 shrink-0 rounded-full bg-warning" />
              <span className="min-w-0 flex-1 truncate">{chat.title || "Untitled chat"}</span>
              <span className="shrink-0 text-meta text-ink-muted">waiting on you</span>
            </RowLink>
          ))}
          {recent
            .filter((chat) => !waiting.includes(chat))
            .map((chat) => (
              <RowLink key={chat.id} href={chatPath(chat.id)} testId={`project-landing-chat-${chat.id}`}>
                <MessageSquare className="size-4 shrink-0 text-ink-muted" aria-hidden="true" />
                <span className="min-w-0 flex-1 truncate">{chat.title || "Untitled chat"}</span>
                {chat.updatedAt && <span className="shrink-0 text-meta text-ink-muted">{formatRelativeTime(chat.updatedAt)}</span>}
              </RowLink>
            ))}
          <RowLink href={projectChatsPath(projectId)}>
            <span className="min-w-0 flex-1 text-ink-muted">All chats</span>
            <ChevronRight className="size-4 text-ink-muted" aria-hidden="true" />
          </RowLink>
        </div>
      )}
    </section>
  );
}
