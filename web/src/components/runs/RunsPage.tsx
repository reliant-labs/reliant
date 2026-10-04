// Copyright (c) 2025 Reliant Labs

/**
 * The Runs tab (/workflows/runs) — every run, whatever started it
 * (WORKFLOW_UI.md §5.2). Rendered inside WorkflowsShell.
 *
 * The one place an hourly automation and an agent-started run are MEANT to be
 * seen, which is why the default is every kind: the chat list hides them, and
 * filtering them out here too would leave them nowhere. Scoped to the current
 * project by default with an "All projects" chip (decision 10).
 */

import { useEffect, useMemo } from "react";
import { useNavigate } from "@tanstack/react-router";
import { Activity, CalendarClock, Workflow } from "lucide-react";

import { runErrorMessage } from "@/api/run-grpc";
import { useChat } from "@/hooks/chat-queries";
import { useRunList } from "@/hooks/run-queries";
import { useTriggers } from "@/hooks/trigger-queries";
import { useProjectStore } from "@/store/projectStore";
import Card from "../forge-ui/card";
import { Button } from "../ui/Button";
import { WORKFLOWS_AUTOMATIONS_PATH, WORKFLOWS_LIBRARY_PATH } from "@/lib/workflowsArea";
import { RunList } from "./RunList";
import { RunFilters, hasActiveRunFilters, useRunsSearch } from "./RunFilters";

export function RunsPage() {
  return <RunsView />;
}

export function RunsView() {
  const [search, setSearch] = useRunsSearch();
  const currentProject = useProjectStore((state) => state.currentProject);
  const projects = useProjectStore((state) => state.projects);
  const loadProjects = useProjectStore((state) => state.loadProjects);
  useEffect(() => {
    void loadProjects().catch(() => undefined);
  }, [loadProjects]);

  // A chat's children (?parent=) are listed wherever and whenever they ran:
  // the parent chat's header promised "N runs started here", and a project
  // scope or the 24-hour default would silently drop some of them.
  const parent = search.parent;
  const scopeProjectId = search.allProjects || parent ? undefined : currentProject?.id;
  const filters = useMemo(
    () => ({
      projectId: scopeProjectId,
      parentChatId: parent,
      state: search.state,
      kind: search.kind,
      workflow: search.workflow,
      trigger: search.trigger,
      range: parent && !search.range ? ("all" as const) : search.range,
      q: search.q,
    }),
    [scopeProjectId, parent, search.state, search.kind, search.workflow, search.trigger, search.range, search.q],
  );
  const parentTitle = useChat(parent).data?.title;
  const list = useRunList(filters);

  // Name the automation a ?trigger= link filters by. Automations are few, and
  // the list query is already cached by the Automations page.
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
    <div className="space-y-5">
      <div className="flex flex-wrap items-end justify-between gap-4">
        <div className="min-w-0">
          <h1 className="text-2xl font-semibold tracking-tight text-foreground">Runs</h1>
          <p className="mt-1 max-w-2xl text-sm text-muted-foreground">
            Every run, whoever started it: your chats, your automations, and runs your agents
            started.
          </p>
        </div>
        <label className="flex items-center gap-2 text-xs text-muted-foreground">
          <input
            type="checkbox"
            checked={groupRepeats}
            onChange={(event) => setSearch({ ...search, group: event.target.checked ? undefined : false })}
            className="h-3.5 w-3.5 rounded border-border accent-primary"
          />
          Group repeated runs
        </label>
      </div>

      <RunFilters
        currentProjectName={currentProject?.name}
        triggerName={triggerName}
        parentTitle={parentTitle}
      />

      {list.isLoading ? (
        <RunListSkeleton />
      ) : list.isError ? (
        <Card padding="lg" role="alert">
          <p className="text-sm font-medium text-foreground">Runs could not be loaded.</p>
          <p className="mt-1 text-sm text-muted-foreground">{runErrorMessage(list.error)}</p>
          <Button className="mt-4" variant="outline" onClick={() => void list.refetch()}>
            Try again
          </Button>
        </Card>
      ) : list.runs.length === 0 ? (
        filtered ? (
          <NoMatchingRuns />
        ) : (
          <NoRuns />
        )
      ) : (
        <RunList
          runs={list.runs}
          groupRepeats={groupRepeats}
          projectNames={projectNames}
          footer={
            list.isFetchNextPageError ? (
              <div className="flex items-center justify-center gap-2 border-t border-border/60 px-5 py-3 text-sm" role="alert">
                <span className="text-muted-foreground">Couldn't load more.</span>
                <button
                  type="button"
                  onClick={() => void list.fetchNextPage()}
                  className="font-medium text-primary hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
                >
                  Retry
                </button>
              </div>
            ) : list.hasNextPage ? (
              <div className="flex justify-center border-t border-border/60 px-5 py-3">
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
  );
}

function RunListSkeleton() {
  // Eight rows in the Finished shape (§5.5). No section labels: the Live
  // section must not pop in above content the user has started reading.
  return (
    <Card padding="none" aria-busy="true" aria-label="Loading runs">
      {Array.from({ length: 8 }, (_, row) => (
        <div key={row} className="flex items-center gap-3 border-b border-border/60 px-5 py-3.5 last:border-b-0">
          <div className="h-2.5 w-2.5 rounded-full bg-border" />
          <div className="flex-1 space-y-1.5">
            <div className="h-3.5 w-56 max-w-full animate-pulse rounded bg-border motion-reduce:animate-none" />
            <div className="h-3 w-40 max-w-full animate-pulse rounded bg-border/60 motion-reduce:animate-none" />
          </div>
          <div className="hidden h-3 w-20 animate-pulse rounded bg-border/60 motion-reduce:animate-none md:block" />
        </div>
      ))}
    </Card>
  );
}

function NoRuns() {
  const navigate = useNavigate();
  return (
    <Card padding="lg" className="flex flex-col items-center px-6 py-14 text-center">
      <div className="flex h-12 w-12 items-center justify-center rounded-full border border-border/60 bg-background text-muted-foreground">
        <Activity className="h-6 w-6" aria-hidden="true" />
      </div>
      <h2 className="mt-4 text-base font-semibold text-foreground">No runs yet</h2>
      <p className="mt-2 max-w-md text-sm text-muted-foreground">
        Runs appear here when you start a chat, run a workflow, or an automation fires.
      </p>
      <div className="mt-6 flex flex-wrap justify-center gap-2">
        <Button variant="primary" leftIcon={<Workflow className="h-4 w-4" />} onClick={() => void navigate({ to: WORKFLOWS_LIBRARY_PATH })}>
          Run a workflow
        </Button>
        <Button variant="outline" leftIcon={<CalendarClock className="h-4 w-4" />} onClick={() => void navigate({ to: WORKFLOWS_AUTOMATIONS_PATH })}>
          Create an automation
        </Button>
      </div>
    </Card>
  );
}

function NoMatchingRuns() {
  const [search, setSearch] = useRunsSearch();
  const range = search.range;
  const clear = () => setSearch({ allProjects: search.allProjects, group: search.group, project: search.project });
  const widen = () => setSearch({ ...search, range: "30d" });
  const windowLabel = range === "7d" ? "7 days" : range === "30d" ? "30 days" : range === "all" ? undefined : "24 hours";

  return (
    <Card padding="lg" className="px-6 py-10 text-center">
      <h2 className="text-base font-semibold text-foreground">No runs match these filters</h2>
      {windowLabel && (
        <p className="mt-2 text-sm text-muted-foreground">
          Showing the last {windowLabel}.
          {range !== "30d" && (
            <>
              {" "}
              <button
                type="button"
                onClick={widen}
                className="font-medium text-primary hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
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
    </Card>
  );
}
