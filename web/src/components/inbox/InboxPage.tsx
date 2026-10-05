// Copyright (c) 2025 Reliant Labs

/**
 * /inbox — everything waiting on the user (WORKFLOW_UI.md §8), in the current
 * project by default, with every project one click away. Interactive chats
 * awaiting an answer are here too (§14.1 decision 2): the sidebar says
 * *where*, the Inbox says *everything*.
 *
 * Items are SECTIONED BY KIND, in the server's priority order (blocking kinds
 * first). The section heading says once what each row in it asks for —
 * "Questions · 3" — so a row is just the run's name, a detail, and its action.
 * An earlier layout put the verb on every row ("Answer a question in …"),
 * which repeated the same words down the page and spent three lines a row.
 *
 * Every row can be dismissed, and so can a whole section. The rows leave at
 * once and a toast offers Undo, which restores them on the server.
 */

import { useMemo } from "react";

import { InboxItemKind, type InboxItem as InboxItemData } from "@/gen/reliant/v1/inbox_pb";
import {
  useDismissInboxItems,
  useInbox,
  useInboxProjectId,
  useInboxScopeStore,
  useRestoreInboxItems,
  type InboxScope,
} from "@/hooks/inbox-queries";
import { toast } from "@/lib/toast-manager";
import { cn } from "@/lib/utils";
import { useProjectStore } from "@/store/projectStore";
import Card from "../forge-ui/card";
import PageHeader from "../forge-ui/page_header";
import SkeletonLoader from "../forge-ui/skeleton_loader";
import { Button } from "../ui/Button";
import { AreaShell } from "../Layout/AreaShell";
import { InboxItem, subjectName } from "./InboxItem";

export function InboxPage() {
  return (
    <AreaShell areaPath="/inbox" areaLabel="Inbox" areaNoun="inbox">
      <InboxView />
    </AreaShell>
  );
}

/** One kind's items, under one heading. */
export interface InboxSection {
  kind: InboxItemKind;
  /** The heading: what every row in the section asks for. */
  title: string;
  items: InboxItemData[];
}

const SECTION_TITLE: Record<InboxItemKind, string> = {
  [InboxItemKind.UNSPECIFIED]: "Other",
  [InboxItemKind.APPROVAL]: "Approvals",
  [InboxItemKind.QUESTION]: "Questions",
  [InboxItemKind.WAITING_FOR_MACHINE]: "Waiting for a machine",
  [InboxItemKind.AUTOMATION_FAILING]: "Failing automations",
  [InboxItemKind.AUTOMATION_LAUNCH_FAILED]: "Automations that could not start",
  [InboxItemKind.RUN_FINISHED]: "Finished runs",
};

/** Section the items by kind, in kind (priority) order, keeping each kind's server order. */
export function sectionInboxItems(items: InboxItemData[]): InboxSection[] {
  const byKind = new Map<InboxItemKind, InboxItemData[]>();
  for (const item of items) {
    const list = byKind.get(item.kind);
    if (list) list.push(item);
    else byKind.set(item.kind, [item]);
  }
  return [...byKind.entries()]
    .sort(([a], [b]) => a - b)
    .map(([kind, sectionItems]) => ({ kind, title: SECTION_TITLE[kind] ?? "Other", items: sectionItems }));
}

export function InboxView() {
  const scope = useInboxScopeStore((state) => state.scope);
  const setScope = useInboxScopeStore((state) => state.setScope);
  const currentProject = useProjectStore((state) => state.currentProject);
  const projectId = useInboxProjectId();
  const inbox = useInbox(projectId);
  const dismiss = useDismissInboxItems();
  const restore = useRestoreInboxItems();

  const items = useMemo(() => inbox.data?.items ?? [], [inbox.data]);
  const sections = useMemo(() => sectionInboxItems(items), [items]);
  const scoped = projectId !== undefined;

  const onDismiss = (dismissed: InboxItemData[], what: string) => {
    const ids = dismissed.map((item) => item.itemId);
    dismiss.mutate(ids, {
      onSuccess: () =>
        toast.notify(`Dismissed ${what}`, {
          action: { label: "Undo", onClick: () => restore.mutate(ids) },
        }),
      onError: (error) => void toast.error(error),
    });
  };

  const header = (
    <PageHeader
      title="Inbox"
      subtitle={scoped ? `Waiting on you in ${currentProject?.name ?? "this project"}.` : "Waiting on you, across every project."}
      className="mb-0"
    />
  );

  const toolbar = currentProject ? (
    <ScopeToggle scope={scope} projectName={currentProject.name} onChange={setScope} />
  ) : null;

  let body;
  if (inbox.isLoading) {
    body = (
      <Card padding="sm" aria-busy="true" aria-label="Loading inbox">
        <SkeletonLoader variant="list-item" count={4} />
      </Card>
    );
  } else if (inbox.isError) {
    body = (
      <Card padding="lg" role="alert">
        <p className="text-sm font-medium text-foreground">The inbox could not be loaded.</p>
        <p className="mt-1 text-sm text-muted-foreground">
          {inbox.error instanceof Error ? inbox.error.message : "Something went wrong."}
        </p>
        <Button className="mt-4" variant="outline" onClick={() => void inbox.refetch()}>
          Try again
        </Button>
      </Card>
    );
  } else if (items.length === 0) {
    // The success state: calm, nothing to do (§8.3). When scoped, say what
    // the other projects hold so "empty" is never a false all-clear.
    const elsewhere = inbox.data?.otherProjectsCount ?? 0;
    body = (
      <Card padding="lg">
        <p className="text-sm font-medium text-foreground">Nothing needs you{scoped ? " in this project" : ""}.</p>
        <p className="mt-1 text-sm text-muted-foreground">
          Approvals, questions and automation problems show up here.
        </p>
        {scoped && elsewhere > 0 && (
          <Button className="mt-4" size="sm" variant="outline" onClick={() => setScope("all")}>
            {elsewhere} waiting in other projects
          </Button>
        )}
      </Card>
    );
  } else {
    body = (
      <div className="space-y-4">
        {sections.map((section) => (
          <SectionCard
            key={section.kind}
            section={section}
            showProject={!scoped}
            onDismissRow={(item) => onDismiss([item], `“${subjectName(item)}”`)}
            onDismissAll={() =>
              onDismiss(section.items, `${section.items.length} ${section.title.toLowerCase()}`)
            }
          />
        ))}
        {(inbox.data?.truncated || (scoped && (inbox.data?.otherProjectsCount ?? 0) > 0)) && (
          <p className="text-xs text-muted-foreground">
            {inbox.data?.truncated && "More items are waiting than are shown. Clear some of these to see the rest. "}
            {scoped && (inbox.data?.otherProjectsCount ?? 0) > 0 && (
              <button
                type="button"
                onClick={() => setScope("all")}
                className="font-medium text-foreground underline-offset-2 hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
              >
                {inbox.data?.otherProjectsCount} more in other projects
              </button>
            )}
          </p>
        )}
      </div>
    );
  }

  return (
    <div className="space-y-5">
      <div className="flex flex-wrap items-end justify-between gap-3">
        {header}
        {toolbar}
      </div>
      {body}
    </div>
  );
}

function ScopeToggle({
  scope,
  projectName,
  onChange,
}: {
  scope: InboxScope;
  projectName: string;
  onChange: (scope: InboxScope) => void;
}) {
  const option = (value: InboxScope, label: string, title?: string) => (
    <button
      type="button"
      role="radio"
      aria-checked={scope === value}
      onClick={() => onChange(value)}
      title={title}
      className={cn(
        "h-7 max-w-48 truncate rounded px-2.5 text-xs font-medium transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40",
        scope === value ? "bg-card text-foreground shadow-sm" : "text-muted-foreground hover:text-foreground",
      )}
    >
      {label}
    </button>
  );
  return (
    <div role="radiogroup" aria-label="Show items from" className="inline-flex rounded-md border border-border/60 bg-background p-0.5">
      {option("project", projectName, `Only ${projectName}`)}
      {option("all", "All projects")}
    </div>
  );
}

function SectionCard({
  section,
  showProject,
  onDismissRow,
  onDismissAll,
}: {
  section: InboxSection;
  showProject: boolean;
  onDismissRow: (item: InboxItemData) => void;
  onDismissAll: () => void;
}) {
  const headingId = `inbox-section-${section.kind}`;
  return (
    <section aria-labelledby={headingId} data-testid={`inbox-section-${section.kind}`}>
      <div className="mb-1.5 flex items-center justify-between gap-3 px-1">
        <h2 id={headingId} className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
          {section.title}
          <span className="ml-1.5 tabular-nums text-muted-foreground/80">{section.items.length}</span>
        </h2>
        {section.items.length > 1 && (
          <button
            type="button"
            onClick={onDismissAll}
            className="rounded px-1.5 text-xs text-muted-foreground transition-colors hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
          >
            Dismiss all
          </button>
        )}
      </div>
      <Card padding="none" className="overflow-hidden">
        <ul aria-labelledby={headingId} className="divide-y divide-border/60">
          {section.items.map((item) => (
            <InboxItem key={item.itemId} item={item} showProject={showProject} onDismiss={onDismissRow} />
          ))}
        </ul>
      </Card>
    </section>
  );
}
