// Copyright (c) 2025 Reliant Labs

/**
 * The Runs tab (/workflows/runs) — every run, whatever started it
 * (WORKFLOW_UI.md §5.2). Rendered inside WorkflowsShell.
 *
 * The one place an hourly automation and an agent-started run are MEANT to be
 * seen, which is why the default is every kind: the chat list hides them, and
 * filtering them out here too would leave them nowhere. It is the log of
 * trigger events that started no run too (Skipped, Couldn't start), shown
 * among the finished runs while no state filter is set. Scoped like every
 * page of the area by the header's project menu (useWorkflowsScope).
 */

import { useMemo } from "react";
import { useNavigate } from "@tanstack/react-router";
import { Activity, CalendarClock, Workflow } from "lucide-react";

import { runErrorMessage } from "@/hooks/run-queries";
import { useChat } from "@/hooks/chat-queries";
import { useRunList } from "@/hooks/run-queries";
import { useTriggers } from "@/hooks/trigger-queries";
import { useProjects } from "@/hooks/project-queries";
import PageHeader from "../forge-ui/page_header";
import Button from "@/components/forge-ui/button";
import { openAutomationDialog } from "../Automations/dialog/openAutomationDialog";
import { useWorkflowsScope } from "../workflows/useWorkflowsScope";
import { RunList } from "./RunList";
import { RunFilters, useRunsSearch } from "./RunFilters";
import { RunsHeadline } from "./RunsHeadline";
import { DEFAULT_RUN_RANGE } from "./runFilterState";
import { clearRunFilters, hasActiveRunFilters } from "./runFilterState";
import { workflowLibraryPath } from "@/lib/paths";
import { ProjectEmptyCtaRow } from "@/components/EmptyState/ProjectEmptyCta";

export function RunsPage() {
  return <RunsView />;
}

export function RunsView() {
  const [search] = useRunsSearch();
  const scope = useWorkflowsScope();
  const { projects } = useProjects();

  // A chat's children (?parent=) are listed wherever and whenever they ran:
  // the parent chat's header promised "N runs started here", and a project
  // scope or the 24-hour default would silently drop some of them.
  const parent = search.parent;
  const scopeProjectId = parent ? undefined : scope.projectId;
  const scopeProjectIds = parent ? undefined : scope.projectIds;
  const filters = useMemo(
    () => ({
      projectId: scopeProjectId,
      projectIds: scopeProjectIds,
      parentChatId: parent,
      state: search.state,
      kind: search.kind,
      workflow: search.workflow,
      trigger: search.trigger,
      range: parent && !search.range ? ("all" as const) : search.range,
      q: search.q,
      includeUnlaunched: true,
    }),
    [scopeProjectId, scopeProjectIds, parent, search.state, search.kind, search.workflow, search.trigger, search.range, search.q],
  );
  const parentTitle = useChat(parent).data?.title;
  const list = useRunList(filters);

  // Name the trigger a ?trigger= link filters by. Triggers are few, and the
  // every-project list is already cached by the Workflows tab.
  const triggers = useTriggers();
  const triggerName = search.trigger
    ? triggers.data?.find((trigger) => trigger.id === search.trigger)?.name
    : undefined;

  const projectNames = useMemo(
    () => (scopeProjectId ? undefined : new Map(projects.map((p) => [p.id, p.name]))),
    [projects, scopeProjectId],
  );
  const groupRepeats = search.group !== false;
  const filtered = hasActiveRunFilters(search);

  return (
    <div className="space-y-8">
      <div className="forge-ui">
        <PageHeader
          className=""
          title="Runs"
          subtitle="Every run, whoever started it: your chats, your automations, and runs your agents started."
        />
      </div>

      {!list.isLoading && !list.isError && list.runs.length > 0 && (
        <RunsHeadline runs={list.runs} range={filters.range ?? DEFAULT_RUN_RANGE} more={!!list.hasNextPage} />
      )}

      <div className="space-y-3">
      <RunFilters triggerName={triggerName} parentTitle={parentTitle} />

      {list.isLoading ? (
        <RunListSkeleton />
      ) : list.isError ? (
        <div role="alert" className="border-t border-border pt-6">
          <p className="text-section text-foreground">Runs could not be loaded.</p>
          <p className="mt-1 text-ui text-muted-foreground">{runErrorMessage(list.error)}</p>
          <Button className="mt-4" variant="outline" onClick={() => void list.refetch()}>
            Try again
          </Button>
        </div>
      ) : list.runs.length === 0 && list.events.length === 0 ? (
        filtered ? (
          <NoMatchingRuns />
        ) : (
          <NoRuns />
        )
      ) : (
        <RunList
          runs={list.runs}
          events={list.events}
          groupRepeats={groupRepeats}
          projectNames={projectNames}
          footer={
            list.isFetchNextPageError ? (
              <div className="flex items-center justify-center gap-2 border-t border-border-subtle px-5 py-3 text-ui" role="alert">
                <span className="text-muted-foreground">Couldn't load more.</span>
                <button
                  type="button"
                  onClick={() => void list.fetchNextPage()}
                  className="font-medium text-primary-ink hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                >
                  Retry
                </button>
              </div>
            ) : list.hasNextPage ? (
              <div className="flex justify-center border-t border-border-subtle px-5 py-3">
                <Button
                  variant="ghost"
                  size="sm"
                  loading={list.isFetchingNextPage}
                  onClick={() => void list.fetchNextPage()}
                >
                  Load more
                </Button>
              </div>
            ) : null
          }
        />
      )}
      </div>
    </div>
  );
}

function RunListSkeleton() {
  // Eight rows in the Finished shape (§5.5). No section labels: the Live
  // section must not pop in above content the user has started reading.
  return (
    <div className="border-t border-border" aria-busy="true" aria-label="Loading runs">
      {Array.from({ length: 8 }, (_, row) => (
        <div key={row} className="flex items-center gap-3 border-b border-border-subtle px-3 py-3.5 last:border-b-0">
          <div className="h-2.5 w-2.5 rounded-full bg-border" />
          <div className="flex-1 space-y-1.5">
            <div className="h-3.5 w-56 max-w-full animate-pulse rounded bg-border motion-reduce:animate-none" />
            <div className="h-3 w-40 max-w-full animate-pulse rounded bg-border/60 motion-reduce:animate-none" />
          </div>
          <div className="hidden h-3 w-20 animate-pulse rounded bg-border/60 motion-reduce:animate-none md:block" />
        </div>
      ))}
    </div>
  );
}

function NoRuns() {
  const navigate = useNavigate();
  return (
    // A designed empty state: an icon tile, what goes here, one way to start (and one quieter one).
    <div className="flex flex-col items-center px-6 py-16 text-center">
      <div className="flex h-12 w-12 items-center justify-center rounded-xl border border-border text-muted-foreground">
        <Activity className="h-6 w-6" aria-hidden="true" />
      </div>
      <h2 className="mt-4 text-section text-foreground">No runs yet</h2>
      <p className="mt-2 max-w-md text-ui text-muted-foreground">
        Runs appear here when you start a chat, run a workflow, or a trigger fires.
      </p>
      <div className="mt-6 flex flex-wrap justify-center gap-2">
        <Button variant="primary" leftIcon={<Workflow className="h-4 w-4" />} onClick={() => void navigate({ to: workflowLibraryPath() })}>
          Run a workflow
        </Button>
        <Button variant="ghost" leftIcon={<CalendarClock className="h-4 w-4" />} onClick={() => openAutomationDialog({})}>
          Make one run by itself
        </Button>
      </div>
      {/* The project shell's other ways in (U6d): an agent deploys with forge, or Move to Forge brings an app over. */}
      <ProjectEmptyCtaRow learn="workflows" className="mt-3 justify-center" testId="runs-empty-cta" />
    </div>
  );
}

function NoMatchingRuns() {
  const [search, setSearch] = useRunsSearch();
  const range = search.range;
  const clear = () => setSearch(clearRunFilters(search));
  const widen = () => setSearch({ ...search, range: "30d" });
  const windowLabel = range === "7d" ? "7 days" : range === "30d" ? "30 days" : range === "all" ? undefined : "24 hours";

  return (
    <div className="border-t border-border px-6 py-12 text-center">
      <h2 className="text-section text-foreground">No runs match these filters</h2>
      {windowLabel && (
        <p className="mt-2 text-ui text-muted-foreground">
          Showing the last {windowLabel}.
          {range !== "30d" && (
            <>
              {" "}
              <button
                type="button"
                onClick={widen}
                className="font-medium text-primary-ink hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
              >
                Widen to 30 days
              </button>
            </>
          )}
        </p>
      )}
      <Button className="mt-5" variant="outline" onClick={clear}>
        Clear filters
      </Button>
    </div>
  );
}
