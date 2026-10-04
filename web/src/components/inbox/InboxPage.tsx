// Copyright (c) 2025 Reliant Labs

/**
 * /inbox — everything waiting on the user, across every project and launch
 * kind (WORKFLOW_UI.md §8). Interactive chats awaiting an answer are here too
 * (§14.1 decision 2): the sidebar says *where*, the Inbox says *everything*.
 *
 * Items arrive in the server's priority order (blocking kinds first, then
 * longest-waiting). Items of one run collapse under one heading ("3 approvals
 * waiting"); a group sits where its first item would have.
 */

import { Link } from "@tanstack/react-router";

import { InboxItemKind, type InboxItem as InboxItemData } from "@/gen/reliant/v1/inbox_pb";
import { useInbox } from "@/hooks/inbox-queries";
import Card from "../forge-ui/card";
import PageHeader from "../forge-ui/page_header";
import SkeletonLoader from "../forge-ui/skeleton_loader";
import { Button } from "../ui/Button";
import { AreaShell } from "../Layout/AreaShell";
import { InboxItem } from "./InboxItem";

export function InboxPage() {
  return (
    <AreaShell areaPath="/inbox" areaLabel="Inbox" areaNoun="inbox">
      <InboxView />
    </AreaShell>
  );
}

/** A run's items, or a single item that belongs to no group. */
export interface InboxGroup {
  key: string;
  items: InboxItemData[];
}

/**
 * Group items that share a run_id, keeping the server's order: a group takes
 * the position of its first item. Items without a run (automation failures)
 * stand alone.
 */
export function groupInboxItems(items: InboxItemData[]): InboxGroup[] {
  const groups: InboxGroup[] = [];
  const byRun = new Map<string, InboxGroup>();
  for (const item of items) {
    if (!item.runId) {
      groups.push({ key: `item:${item.itemId}`, items: [item] });
      continue;
    }
    const existing = byRun.get(item.runId);
    if (existing) {
      existing.items.push(item);
      continue;
    }
    const group = { key: item.runId, items: [item] };
    byRun.set(item.runId, group);
    groups.push(group);
  }
  return groups;
}

const KIND_NOUN: Partial<Record<InboxItemKind, [string, string]>> = {
  [InboxItemKind.APPROVAL]: ["approval", "approvals"],
  [InboxItemKind.QUESTION]: ["question", "questions"],
  [InboxItemKind.WAITING_FOR_MACHINE]: ["machine wait", "machine waits"],
};

/** "3 approvals waiting", or "2 approvals and 1 question waiting". */
export function groupSummary(items: InboxItemData[]): string {
  const counts = new Map<InboxItemKind, number>();
  for (const item of items) counts.set(item.kind, (counts.get(item.kind) ?? 0) + 1);
  const parts = [...counts.entries()].map(([kind, count]) => {
    const [one, many] = KIND_NOUN[kind] ?? ["item", "items"];
    return `${count} ${count === 1 ? one : many}`;
  });
  const joined = parts.length > 1 ? `${parts.slice(0, -1).join(", ")} and ${parts[parts.length - 1]}` : parts[0];
  return `${joined} waiting`;
}

export function InboxView() {
  const inbox = useInbox();

  const header = (
    <PageHeader title="Inbox" subtitle="Approvals, questions and automation problems, across every project." />
  );

  if (inbox.isLoading) {
    return (
      <div className="space-y-6">
        {header}
        <Card padding="md" aria-busy="true" aria-label="Loading inbox">
          <SkeletonLoader variant="list-item" count={3} />
        </Card>
      </div>
    );
  }

  if (inbox.isError) {
    return (
      <div className="space-y-6">
        {header}
        <Card padding="lg" role="alert">
          <p className="text-sm font-medium text-foreground">The inbox could not be loaded.</p>
          <p className="mt-1 text-sm text-muted-foreground">
            {inbox.error instanceof Error ? inbox.error.message : "Something went wrong."}
          </p>
          <Button className="mt-4" variant="outline" onClick={() => void inbox.refetch()}>
            Try again
          </Button>
        </Card>
      </div>
    );
  }

  const items = inbox.data?.items ?? [];

  if (items.length === 0) {
    // The success state: calm, no illustration, nothing to do (§8.3).
    return (
      <div className="space-y-6">
        {header}
        <Card padding="lg">
          <p className="text-sm font-medium text-foreground">Nothing needs you.</p>
          <p className="mt-1 text-sm text-muted-foreground">
            Approvals, questions and automation problems show up here.
          </p>
        </Card>
      </div>
    );
  }

  const groups = groupInboxItems(items);

  return (
    <div className="space-y-6">
      {header}
      <Card padding="none">
        <ul aria-label="Waiting on you" className="divide-y divide-border/60">
          {groups.map((group) =>
            group.items.length === 1 ? (
              <InboxItem key={group.key} item={group.items[0]!} />
            ) : (
              <RunGroup key={group.key} group={group} />
            ),
          )}
        </ul>
      </Card>
      {inbox.data?.truncated && (
        <p className="text-sm text-muted-foreground">
          More items are waiting than are shown. Clear some of these to see the rest.
        </p>
      )}
    </div>
  );
}

function RunGroup({ group }: { group: InboxGroup }) {
  const first = group.items[0]!;
  const name = first.chatTitle || first.triggerName || "Untitled run";
  return (
    <li data-testid={`inbox-group-${group.key}`}>
      <div className="flex items-baseline justify-between gap-3 px-4 pt-3">
        <Link
          to="/workflows/runs/$runId"
          params={{ runId: first.chatId }}
          className="truncate text-sm font-semibold text-foreground hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
        >
          {name}
        </Link>
        <span className="shrink-0 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
          {groupSummary(group.items)}
        </span>
      </div>
      <ul aria-label={`${name}: ${groupSummary(group.items)}`} className="divide-y divide-border/60">
        {group.items.map((item) => (
          <InboxItem key={item.itemId} item={item} grouped />
        ))}
      </ul>
    </li>
  );
}
