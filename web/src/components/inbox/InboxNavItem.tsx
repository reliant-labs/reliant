// Copyright (c) 2025 Reliant Labs

/**
 * The Inbox's top-level nav entry and its badge (WORKFLOW_UI.md §8.2,
 * §14.1 decision 1).
 *
 * Badge rule: the items that block a live run (approvals, questions, waiting
 * for machine) are a solid NUMBER; automation failures add a DOT, not a count.
 * A blocked run is urgent; a failed one is informational.
 *
 * Counts follow the Inbox's scope (the current project, or every project), so
 * the badge never counts what the Inbox will not show.
 *
 * Counts come from `ListInbox({limit: 0})`, invalidated by the same user
 * updates as the Inbox and refetched on focus. If that read fails, the badge
 * falls back to what the client already knows — listed chats awaiting input,
 * from the activity store — and shows a warning dot to say it may be
 * incomplete (§8.3).
 *
 * Self-contained so the Sidebar hunk that mounts it stays one line.
 */

import { useMemo } from "react";
import { Inbox } from "lucide-react";

import { ChatActivity } from "@/gen/reliant/v1/chat_pb";
import { useInboxCounts, useInboxProjectId } from "@/hooks/inbox-queries";
import { cn } from "@/lib/utils";
import { useActivityStore } from "@/store/activityStore";

interface InboxNavItemProps {
  onOpen?: () => void;
}

export function InboxNavItem({ onOpen }: InboxNavItemProps) {
  const counts = useInboxCounts(useInboxProjectId());
  const activities = useActivityStore((state) => state.activities);
  const knownAwaiting = useMemo(() => {
    let n = 0;
    for (const activity of activities.values()) if (activity === ChatActivity.AWAITING_INPUT) n += 1;
    return n;
  }, [activities]);

  const degraded = counts.isError;
  const count = degraded ? knownAwaiting : (counts.data?.blockingCount ?? 0);
  const informational = !degraded && !!counts.data?.hasInformational;

  const announcement =
    count > 0 ? `${count} ${count === 1 ? "item needs" : "items need"} you` : informational ? "Automation problems" : "";

  return (
    <button
      type="button"
      onClick={onOpen}
      disabled={!onOpen}
      className={cn(
        "flex h-8 w-full items-center gap-2 rounded-md px-2 text-left text-sm transition-colors",
        "text-foreground/85 hover:bg-muted/50 hover:text-foreground",
        "focus:outline-none focus:ring-2 focus:ring-ring/40",
        "disabled:cursor-default disabled:opacity-50 disabled:hover:bg-transparent",
      )}
      data-testid="sidebar-inbox-button"
    >
      <span className="flex h-4 w-4 shrink-0 items-center justify-center text-muted-foreground">
        <Inbox className="h-4 w-4" />
      </span>
      <span className="min-w-0 flex-1 truncate">Inbox</span>
      <span className="flex shrink-0 items-center gap-1">
        {count > 0 && (
          <span
            data-testid="inbox-badge-count"
            aria-hidden="true"
            className="inline-flex h-4 min-w-4 items-center justify-center rounded-full bg-primary px-1 text-2xs font-semibold tabular-nums text-primary-foreground"
          >
            {count}
          </span>
        )}
        {informational && (
          <span data-testid="inbox-badge-dot" aria-hidden="true" className="h-1.5 w-1.5 rounded-full bg-destructive" />
        )}
        {degraded && (
          <span
            data-testid="inbox-badge-warning"
            title="The inbox could not be loaded; this count may be incomplete."
            className="h-1.5 w-1.5 rounded-full bg-warning"
          />
        )}
      </span>
      <span className="sr-only" aria-live="polite" data-testid="inbox-badge-live">
        {announcement}
        {degraded ? " (may be incomplete)" : ""}
      </span>
    </button>
  );
}
