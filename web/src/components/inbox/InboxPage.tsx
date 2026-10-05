// Copyright (c) 2025 Reliant Labs

/**
 * /inbox — everything waiting on the user, across every project and launch
 * kind (WORKFLOW_UI.md §8). Interactive chats awaiting an answer are here too
 * (§14.1 decision 2): the sidebar says *where*, the Inbox says *everything*.
 *
 * Items arrive in the server's priority order (blocking kinds first, then
 * longest-waiting). Items of one run collapse under one heading ("3 approvals
 * waiting"), and so do one automation's informational items (§8.2 "by
 * automation for repeated failures"); a group sits where its first item
 * would have.
 */

import { Link } from "@tanstack/react-router";

import { InboxItemKind, type InboxItem as InboxItemData } from "@/gen/reliant/v1/inbox_pb";
import { useInbox } from "@/hooks/inbox-queries";
import Card from "../forge-ui/card";
import PageHeader from "../forge-ui/page_header";
import SkeletonLoader from "../forge-ui/skeleton_loader";
import { Button } from "../ui/Button";
import { AreaShell } from "../Layout/AreaShell";
import { InboxItem, type InboxItemOmissions } from "./InboxItem";

export function InboxPage() {
  return (
    <AreaShell areaPath="/inbox" areaLabel="Inbox" areaNoun="inbox">
      <InboxView />
    </AreaShell>
  );
}

/** A run's items, an automation's, or a single item that belongs to no group. */
export interface InboxGroup {
  key: string;
  /** What the items share: "run" groups blocking items, "automation" the rest. */
  by: "run" | "automation" | "none";
  items: InboxItemData[];
}

/**
 * The informational kinds an automation produces. The server dedupes each
 * kind to one item per failure episode (#432), but one automation can still
 * leave several rows: a failed launch counts as a failure, so two failed
 * launches make it both Failing and launch-failed; and an automation that
 * opted in to "notify me" leaves one run-finished row per unread run.
 */
function isAutomationInformational(item: InboxItemData): boolean {
  return (
    !!item.triggerId &&
    (item.kind === InboxItemKind.AUTOMATION_FAILING ||
      item.kind === InboxItemKind.AUTOMATION_LAUNCH_FAILED ||
      item.kind === InboxItemKind.RUN_FINISHED)
  );
}

/**
 * Group items, keeping the server's order: a group takes the position of its
 * first item.
 *
 *   - An automation's informational items group by trigger_id.
 *   - Everything else that has a run groups by run_id: those items block a
 *     live run, and the run is what the user acts in, so a blocking item of an
 *     automation's run is never folded in with that automation's failures.
 *   - Anything else stands alone.
 */
export function groupInboxItems(items: InboxItemData[]): InboxGroup[] {
  const groups: InboxGroup[] = [];
  const byKey = new Map<string, InboxGroup>();
  for (const item of items) {
    const [key, by] = isAutomationInformational(item)
      ? [`trigger:${item.triggerId}`, "automation" as const]
      : item.runId
        ? [item.runId, "run" as const]
        : [null, "none" as const];
    if (key === null) {
      groups.push({ key: `item:${item.itemId}`, by, items: [item] });
      continue;
    }
    const existing = byKey.get(key);
    if (existing) {
      existing.items.push(item);
      continue;
    }
    const group = { key, by, items: [item] };
    byKey.set(key, group);
    groups.push(group);
  }
  return groups;
}

const normalizeDetail = (text: string) => text.trim().replace(/\s+/g, " ").toLowerCase();

/**
 * What each row in one automation's group can leave out because a sibling
 * already says it. A failed launch counts as a failure, so the same streak
 * yields a "failing" row AND a "could not start" row, and both would read
 * "Failed 3 times in a row. <the same reason>". The failing row keeps the
 * streak count; the launch row keeps the reason; neither repeats the other.
 */
export function automationGroupOmissions(items: InboxItemData[]): Map<string, InboxItemOmissions> {
  const omissions = new Map<string, InboxItemOmissions>();
  const failing = items.find((item) => item.payload.case === "automationFailing");
  const launch = items.find((item) => item.payload.case === "automationLaunchFailed");
  if (!failing || !launch) return omissions;
  if (failing.payload.case !== "automationFailing" || launch.payload.case !== "automationLaunchFailed") return omissions;
  const detail = failing.payload.value.health?.lastFailureDetail ?? "";
  const reason = launch.payload.value.reason;
  if (detail && normalizeDetail(detail) === normalizeDetail(reason)) {
    omissions.set(failing.itemId, { detail: true });
  }
  omissions.set(launch.itemId, { streak: true });
  return omissions;
}

/** "Failing · could not start · 2 runs finished": what one automation has waiting. */
export function automationGroupSummary(items: InboxItemData[]): string {
  const count = (kind: InboxItemKind) => items.filter((item) => item.kind === kind).length;
  const finished = count(InboxItemKind.RUN_FINISHED);
  const parts = [
    count(InboxItemKind.AUTOMATION_FAILING) > 0 ? "failing" : null,
    count(InboxItemKind.AUTOMATION_LAUNCH_FAILED) > 0 ? "could not start" : null,
    finished > 0 ? `${finished} ${finished === 1 ? "run" : "runs"} finished` : null,
  ].filter((part): part is string => part !== null);
  const joined = parts.join(" · ");
  return joined.charAt(0).toUpperCase() + joined.slice(1);
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
            ) : group.by === "automation" ? (
              <AutomationGroup key={group.key} group={group} />
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
          <InboxItem key={item.itemId} item={item} groupedBy="run" />
        ))}
      </ul>
    </li>
  );
}

/** One automation's failures and finished runs, under the automation's name. */
function AutomationGroup({ group }: { group: InboxGroup }) {
  const first = group.items[0]!;
  const name = first.triggerName || "Automation";
  const summary = automationGroupSummary(group.items);
  const omissions = automationGroupOmissions(group.items);
  return (
    <li data-testid={`inbox-group-${group.key}`}>
      <div className="flex items-baseline justify-between gap-3 px-4 pt-3">
        <Link
          to="/workflows/automations/$triggerId"
          params={{ triggerId: first.triggerId }}
          className="truncate text-sm font-semibold text-foreground hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
        >
          {name}
        </Link>
        <span className="shrink-0 text-xs font-semibold uppercase tracking-wide text-muted-foreground">{summary}</span>
      </div>
      <ul aria-label={`${name}: ${summary}`} className="divide-y divide-border/60">
        {group.items.map((item) => (
          <InboxItem key={item.itemId} item={item} groupedBy="automation" omit={omissions.get(item.itemId)} />
        ))}
      </ul>
    </li>
  );
}
