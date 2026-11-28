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

import type { ReactNode } from "react";
import { Inbox } from "lucide-react";

import { ChatActivity } from "@/gen/reliant/v1/chat_pb";
import { useInboxCounts, useInboxProjectId } from "@/hooks/inbox-queries";
import { useActivityStore } from "@/store/activityStore";

import { SidebarRow } from "../Layout/sidebar/primitives";
import { useShellCurrentProject } from "../Layout/sidebar/useShellCurrentProject";

interface InboxNavItemProps {
  onOpen?: () => void;
  /** The Inbox's path: given, the row is a link (a modified click opens a new tab). */
  href?: string;
  /** The Inbox is the page on screen. */
  current?: boolean;
  /** The row's mark; the inbox tray by default (the shell's footer draws a bell). */
  icon?: ReactNode;
}

/** What the Inbox badge shows: the blocking count, the failures dot, and whether the read failed. */
export interface InboxBadgeState {
  /** Items blocking a live run (a solid number), or what the client knows when degraded. */
  count: number;
  /** Automation failures, which add a dot rather than a count. */
  informational: boolean;
  /** The counts read failed; `count` is the client's partial view. */
  degraded: boolean;
  /** Screen-reader text for the badge, or "" when there is nothing to say. */
  announcement: string;
}

/**
 * The badge rule above, shared by every Inbox entry point: the desktop
 * sidebar row, the mobile drawer row and the mobile menu button.
 */
export function useInboxBadge(): InboxBadgeState {
  // The project the Inbox page shows (the shell's), so the badge never counts
  // every project while the page shows one, a cold /inbox included.
  const counts = useInboxCounts(useInboxProjectId(useShellCurrentProject()?.id ?? null));
  // Selects the count, not the map: the map is replaced on every activity
  // change of every chat, and this hook backs a badge on every mobile screen
  // header as well as the drawer and the desktop sidebar. A number only
  // changes — and only re-renders them — when the count does.
  const knownAwaiting = useActivityStore((state) => {
    let n = 0;
    for (const activity of state.activities.values()) if (activity === ChatActivity.AWAITING_INPUT) n += 1;
    return n;
  });

  const degraded = counts.isError;
  const count = degraded ? knownAwaiting : (counts.data?.blockingCount ?? 0);
  const informational = !degraded && !!counts.data?.hasInformational;

  const announcement =
    count > 0 ? `${count} ${count === 1 ? "item needs" : "items need"} you` : informational ? "Automation problems" : "";

  return { count, informational, degraded, announcement };
}

/**
 * Whether a machine has housekeeping waiting — storage, worktree clean-up,
 * reclaim proposals — for the Agent sandboxes nav row's dot. Read from the
 * badge's own query (the same scope, so no second read): the items the
 * person's filters kept out of the Inbox, or the listed ones when their
 * housekeeping filter is on.
 */
export function useSandboxesHousekeeping(): boolean {
  const counts = useInboxCounts(useInboxProjectId(useShellCurrentProject()?.id ?? null));
  return !!counts.data?.hasHousekeeping;
}

/** The dot after the Agent sandboxes nav row while a machine has housekeeping waiting. */
export function SandboxesHousekeepingDot() {
  if (!useSandboxesHousekeeping()) return null;
  return <HousekeepingDot label="A sandbox has housekeeping waiting" testId="sandboxes-housekeeping-dot" />;
}

/**
 * The housekeeping dot: 8px, the action blue — something awaits a decision,
 * nothing is broken, so not a warning or danger colour.
 */
export function HousekeepingDot({ label, testId }: { label: string; testId?: string }) {
  return (
    <span
      role="img"
      aria-label={label}
      title={label}
      data-testid={testId}
      className="inline-block size-2 shrink-0 rounded-full bg-primary"
    />
  );
}

export function InboxNavItem({ onOpen, href, current, icon }: InboxNavItemProps) {
  const { count, informational, degraded, announcement } = useInboxBadge();

  // The badge is the row's trail (restyle §11.3): the count in meta, a dot
  // for failures, a warning dot when the count may be incomplete.
  const trail =
    count > 0 || informational || degraded ? (
      <>
        {count > 0 && (
          <span data-testid="inbox-badge-count" aria-hidden="true" className="tabular-nums">
            {count}
          </span>
        )}
        {informational && (
          <span data-testid="inbox-badge-dot" aria-hidden="true" className="size-2 rounded-full bg-destructive" />
        )}
        {degraded && (
          <span
            data-testid="inbox-badge-warning"
            title="The inbox could not be loaded; this count may be incomplete."
            className="size-2 rounded-full bg-warning"
          />
        )}
      </>
    ) : undefined;

  return (
    <SidebarRow
      icon={icon ?? <Inbox className="size-4" />}
      label="Inbox"
      onClick={onOpen}
      href={href}
      current={current}
      trail={trail}
      // Named by its content: "Inbox" and the live count below.
      ariaLabel={null}
      testId="sidebar-inbox-button"
    >
      <span className="sr-only" aria-live="polite" data-testid="inbox-badge-live">
        {announcement}
        {degraded ? " (may be incomplete)" : ""}
      </span>
    </SidebarRow>
  );
}
