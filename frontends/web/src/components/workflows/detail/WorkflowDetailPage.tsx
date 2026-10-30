// Copyright (c) 2025 Reliant Labs

/**
 * /workflows/library/$workflowRef — one workflow: what it does, how it is
 * used, and Run… (WORKFLOW_UI.md §2.3). Every surface that names a workflow
 * can link here, and every link to an automation lands here, on its trigger.
 *
 * It is the ONE place the workflow is configured to run by itself: the
 * Triggers card lists every trigger — declared in the workflow file or added
 * by hand — per project, with its switch, Settings or Edit, Fire trigger now
 * and its history; a callout under the header names the worst one that needs
 * the user.
 *
 * Main column: Triggers, recent runs (the Runs tab's own RunList and run log,
 * so the two cannot drift — trigger events that started no run included),
 * and the definition — its diagram, read-only, and its typed inputs. Side
 * rail: About, this workflow's Presets, and Used by: the workflows that call
 * this one through `ref:`.
 *
 * Both cards honour the area's scope (one project, or every project). The
 * header holds Run… (attended: the run form) and Edit only.
 */

import { useMemo, useState } from "react";
import { Link, useNavigate, useParams } from "@tanstack/react-router";
import { Pencil, Play } from "lucide-react";

import type { WorkflowResponse } from "@/hooks/workflow-queries";
import { isChatLaunchable } from "@/hooks/workflow-queries";
import { useRunList } from "@/hooks/run-queries";
import { useTriggers } from "@/hooks/trigger-queries";
import { useWorkflowLibrary } from "@/hooks/workflow-library-queries";
import { formatInputDefault } from "@/lib/inputDefaultDisplay";
import { getInputDefault, getInputDescription, type InputDef } from "@/lib/inputHelpers";
import { WORKFLOWS_LIBRARY_PATH } from "@/lib/workflowsArea";
import { useScopePreferences } from "@/hooks/settings-queries";
import { useProjectStore } from "@/store/projectStore";
import { useProjects } from "@/hooks/project-queries";
import Card, { CardHeader, CardInset } from "../../forge-ui/card";
import DataTable, { type DataTableColumn } from "../../forge-ui/data_table";
import PageHeader from "../../forge-ui/page_header";
import Button from "@/components/forge-ui/button";
import { AutomationStateNotice } from "../../Automations/AutomationStateNotice";
import { openAutomationDialog } from "../../Automations/dialog/openAutomationDialog";
import { RunList } from "../../runs/RunList";
import { RunNowDialog } from "../../workflow/run/RunNowDialog";
import { WorkflowViewerPanel } from "../../workflow/WorkflowViewerPanel";
import { workflowDisplayName } from "../../../lib/workflowDisplayName";
import { useWorkflowDefinition } from "../../workflow/useWorkflowDefinition";
import { DRAFT_NOT_RUNNABLE } from "../../workflow/workflowDraftStatus";
import { WorkflowBadge, WorkflowSourceBadge } from "../WorkflowSourceBadge";
import { WorkflowPresetsSection } from "./WorkflowPresetsSection";
import { declaredTriggers, describeDeclaredSource } from "@/lib/declaredTriggers";
import { useIntegrationEventNaming } from "@/hooks/catalog-queries";
import { automationUnits, unitsNeedingYou } from "@/lib/automationUnits";
import { describeSchedule } from "@/lib/cronText";
import { triggerRunsWorkflow } from "@/lib/triggerRail";
import { useWorkflowsScope } from "../useWorkflowsScope";
import { TriggersCard } from "./TriggersCard";
import { workflowsUsing } from "./workflowUsedBy";

/** How many runs (and trigger events that started none) Recent runs shows; "View all" opens the Runs tab. */
export const RECENT_RUNS_LIMIT = 10;

export function WorkflowDetailPage() {
  const { workflowRef: rawRef } = useParams({ strict: false }) as { workflowRef?: string };
  const workflowRef = rawRef ? decodeURIComponent(rawRef) : "";
  const projectId = useProjectStore((state) => state.currentProject?.id);
  const projectsLoading = useProjects().isLoading;

  if (!workflowRef) return <WorkflowNotFound />;
  if (!projectId) return projectsLoading ? <DetailSkeleton /> : <WorkflowNotFound />;
  return <WorkflowDetail projectId={projectId} workflowRef={workflowRef} />;
}

/** The area's scope as this page reads it, and the way to widen it. */
function useDetailScope() {
  const scope = useWorkflowsScope();
  const navigate = useNavigate();
  const showAllProjects = () =>
    void navigate({
      to: ".",
      search: (prev: Record<string, unknown>) => ({ ...prev, allProjects: true }),
      replace: true,
    } as never);
  return { scopeProjectId: scope.projectId, showAllProjects };
}

export function WorkflowDetail({ projectId, workflowRef }: { projectId: string; workflowRef: string }) {
  const navigate = useNavigate();
  const { scopeProjectId, showAllProjects } = useDetailScope();
  const library = useWorkflowLibrary(projectId);
  const definition = useWorkflowDefinition(projectId, workflowRef);
  const scopePreferences = useScopePreferences();
  // Unknown until the answer arrives: no "default" badge rather than a guess.
  const defaultWorkflow = scopePreferences.isPending ? undefined : scopePreferences.data.defaultWorkflow;
  const [running, setRunning] = useState(false);
  const allTriggers = useTriggers();
  const naming = useIntegrationEventNaming();

  const listing = library.data?.workflows.find((w) => w.name === workflowRef);
  const invalid = library.data?.invalidWorkflows.find((w) => w.name === workflowRef);

  if (library.isLoading) return <DetailSkeleton />;
  if (library.data && !listing && !invalid) {
    return <WorkflowNotFound workflowRef={workflowRef} projectId={projectId} scopeProjectId={scopeProjectId} />;
  }

  const displayName = workflowDisplayName({ name: workflowRef, title: listing?.title });
  const isDraft = listing?.status === "draft";
  const runBlockedReason = invalid
    ? "This workflow failed to load, so it cannot run."
    : definition.error
      ? "This workflow's definition has errors, so it cannot run."
      : isDraft
        ? `${DRAFT_NOT_RUNNABLE} Open it in the editor to publish it.`
        : null;
  const openBuilder = () =>
    void navigate({ to: "/workflow/$workflowName", params: { workflowName: workflowRef } });
  const declared = definition.workflowDef ? declaredTriggers(definition.workflowDef) : [];
  const setupBlockedReason = invalid
    ? "This workflow failed to load, so it cannot be turned on."
    : isDraft
      ? "This workflow is a draft. Open it in the editor to publish it, then set it up."
      : null;
  // The callout names the worst of its triggers that needs the user, in any
  // project: the same "needs you" the Workflows list pins on top.
  const needsYou = unitsNeedingYou(automationUnits((allTriggers.data ?? []).filter((t) => triggerRunsWorkflow(t, workflowRef)))).find(
    (unit) => unit.health.cta,
  );
  const startsFrom = [
    ...(listing && isChatLaunchable(listing) ? ["Chat"] : []),
    ...declared.map((d) => describeDeclaredSource(d, describeSchedule, naming)),
  ];
  const needsYouProject = needsYou ? (needsYou.kind === "workflow" ? needsYou.projectId : needsYou.trigger.projectId) : "";
  const needsYouProjectName = needsYou
    ? (needsYou.kind === "workflow" ? needsYou.projectName : needsYou.trigger.projectName) ?? "a deleted project"
    : "";

  return (
    <div className="space-y-4" data-testid="workflow-detail">
      <div className="forge-ui">
        <PageHeader
          className=""
          breadcrumbs={[{ label: "Workflows", href: WORKFLOWS_LIBRARY_PATH }, { label: displayName }]}
          title={displayName}
          titleAccessory={
            <>
              {listing && <WorkflowSourceBadge source={listing.source} />}
              {defaultWorkflow === workflowRef && <WorkflowBadge label="Default" variant="info" />}
              {isDraft && <WorkflowBadge label="Draft" variant="warning" />}
            </>
          }
          subtitle={
            listing?.description || runBlockedReason ? (
              <>
                {listing?.description && <span className="block max-w-3xl">{listing.description}</span>}
                {/* The reason is text, not only a tooltip (§2.3). */}
                {runBlockedReason && <span className="mt-1 block text-warning-ink">{runBlockedReason}</span>}
              </>
            ) : undefined
          }
          actions={[
            { label: "Edit", variant: "secondary", icon: <Pencil className="h-4 w-4" aria-hidden="true" />, onClick: openBuilder },
            {
              label: "Run…",
              variant: "primary",
              icon: <Play className="h-4 w-4" aria-hidden="true" />,
              onClick: () => setRunning(true),
              disabled: !!runBlockedReason,
            },
          ]}
        />
      </div>

      {needsYou && (
        <AutomationStateNotice
          health={needsYou.health}
          triggers={needsYou.kind === "workflow" ? needsYou.triggers : [needsYou.trigger]}
          workflowRef={workflowRef}
          projectId={needsYouProject}
          subject={`${displayName} in ${needsYouProjectName}`}
        />
      )}

      <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_18rem]">
        <div className="min-w-0 space-y-4">
          <TriggersCard
            workflowRef={workflowRef}
            workflowTitle={displayName}
            declared={declared}
            projectId={projectId}
            scopeProjectId={scopeProjectId}
            setupBlockedReason={setupBlockedReason}
            onShowAllProjects={showAllProjects}
          />
          <RecentRunsCard
            projectId={scopeProjectId}
            workflowRef={workflowRef}
            onRun={runBlockedReason ? undefined : () => setRunning(true)}
            onAddTrigger={setupBlockedReason ? undefined : () => openAutomationDialog({ workflowRef, projectId, custom: true })}
          />
          <DefinitionCard
            projectId={projectId}
            workflowRef={workflowRef}
            definition={definition}
            invalidErrors={invalid?.errors}
          />
        </div>
        <aside className="min-w-0 space-y-4" aria-label="About this workflow">
          <Card>
            <CardHeader title="About" />
            <dl className="space-y-2 text-sm">
              <AboutRow label="Source" value={listing ? sourceLong(listing.source) : invalid ? sourceLong(invalid.source) : "—"} />
              <AboutRow label="Reference" value={<span className="font-mono text-xs">{workflowRef}</span>} />
              {listing?.updatedAt && <AboutRow label="Last edited" value={new Date(listing.updatedAt).toLocaleString()} />}
              <AboutRow label="Status" value={invalid ? "Failed to load" : isDraft ? "Draft" : "Ready to run"} />
              {startsFrom.length > 0 && <AboutRow label="Starts from" value={<span title={startsFrom.join("; ")}>{startsFrom.join("; ")}</span>} />}
              {listing?.needsMachine && (
                <AboutRow label="Needs a machine" value={listing.needsMachine.length > 0 ? "Yes" : "No: it works through its integrations"} />
              )}
            </dl>
          </Card>
          <WorkflowPresetsSection projectId={projectId} workflowRef={workflowRef} />
          <UsedByCard workflowRef={workflowRef} workflows={library.data?.workflows ?? []} />
        </aside>
      </div>

      <RunNowDialog open={running} onClose={() => setRunning(false)} projectId={projectId} workflowRef={workflowRef} opensIn="run" />
    </div>
  );
}

function sourceLong(source: "builtin" | "user" | "project"): string {
  return source === "builtin" ? "Built-in" : source === "project" ? "This project's repository" : "Yours";
}

/**
 * The workflows that call this one through `ref:` (workflowUsedBy.ts), from
 * the library list detail already has loaded. Each links to its own detail.
 */
function UsedByCard({ workflowRef, workflows }: { workflowRef: string; workflows: WorkflowResponse[] }) {
  const usedBy = useMemo(() => workflowsUsing(workflowRef, workflows), [workflowRef, workflows]);
  return (
    <section aria-label="Used by">
      <Card>
        <CardHeader title="Used by" />
        {usedBy.length === 0 ? (
          <p className="text-sm text-muted-foreground">No other workflow calls this one.</p>
        ) : (
          <ul aria-label="Workflows that call this one" className="space-y-1.5" data-testid="workflow-detail-used-by">
            {usedBy.map((workflow) => (
              <li key={workflow.name} className="flex min-w-0 items-center justify-between gap-2">
                <Link
                  to="/workflows/library/$workflowRef"
                  params={{ workflowRef: workflow.name }}
                  className="truncate rounded-sm text-sm font-medium text-foreground hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
                >
                  {workflowDisplayName(workflow)}
                </Link>
                <WorkflowSourceBadge source={workflow.source} />
              </li>
            ))}
          </ul>
        )}
      </Card>
    </section>
  );
}

function AboutRow({ label, value }: { label: string; value: React.ReactNode }) {
  return (
    <div className="flex items-baseline justify-between gap-3">
      <dt className="shrink-0 text-xs text-muted-foreground">{label}</dt>
      <dd className="min-w-0 truncate text-right text-foreground">{value}</dd>
    </div>
  );
}

function RecentRunsCard({
  projectId,
  workflowRef,
  onRun,
  onAddTrigger,
}: {
  /** The project in scope; undefined for every project. */
  projectId?: string;
  workflowRef: string;
  onRun?: () => void;
  onAddTrigger?: () => void;
}) {
  // Every run of this workflow in scope, whenever it ran and whatever started
  // it — and every trigger event of it that started no run.
  const filters = useMemo(
    () => ({ projectId, workflow: [workflowRef], range: "all" as const, includeUnlaunched: true }),
    [projectId, workflowRef],
  );
  const list = useRunList(filters);
  const runs = list.runs.slice(0, RECENT_RUNS_LIMIT);
  const oldestRun = runs.length === RECENT_RUNS_LIMIT ? runs[runs.length - 1]!.createdAt : 0;
  const events = list.events.filter((e) => e.occurredAt >= oldestRun).slice(0, RECENT_RUNS_LIMIT);

  return (
    <section aria-label="Recent runs">
      <Card padding="none" className="overflow-hidden">
        <div className="flex items-center justify-between gap-3 border-b border-border/60 px-4 py-2.5">
          <h2 className="text-sm font-semibold text-foreground">Recent runs</h2>
          <Link
            to="/workflows/runs"
            search={{ workflow: [workflowRef], range: "all", allProjects: projectId ? undefined : true }}
            className="rounded-sm text-xs font-medium text-primary hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
          >
            View all
          </Link>
        </div>
        {list.isLoading ? (
          <p className="px-4 py-3 text-sm text-muted-foreground" aria-busy="true">Loading runs…</p>
        ) : list.isError ? (
          <p className="px-4 py-3 text-sm text-muted-foreground" role="alert">Runs could not be loaded.</p>
        ) : runs.length === 0 && events.length === 0 ? (
          <div className="flex flex-wrap items-center gap-2 px-4 py-3 text-sm text-muted-foreground">
            <span>Not run yet.</span>
            {onRun && (
              <Button variant="ghost" size="sm" onClick={onRun}>
                Run…
              </Button>
            )}
            {onAddTrigger && (
              <Button variant="ghost" size="sm" onClick={onAddTrigger}>
                Add trigger…
              </Button>
            )}
          </div>
        ) : (
          <div data-testid="workflow-detail-runs">
            <RunList runs={runs} events={events} groupRepeats={false} bare />
          </div>
        )}
      </Card>
    </section>
  );
}

/**
 * The definition: its diagram, read-only, and its typed inputs. ONE header —
 * Edit is the page header's, so neither this card nor the diagram repeats it.
 */
function DefinitionCard({
  projectId,
  workflowRef,
  definition,
  invalidErrors,
}: {
  projectId: string;
  workflowRef: string;
  definition: ReturnType<typeof useWorkflowDefinition>;
  invalidErrors?: string[];
}) {
  const inputs = definition.inputGroups.flatMap((group) =>
    group.inputs.map((input) => ({ ...input, group: group.name })),
  );
  const brokenErrors = invalidErrors ?? (definition.error ? [definition.error] : null);

  return (
    <section aria-label="Definition">
      <Card>
        <CardHeader title="Definition" />
        {brokenErrors ? (
          <CardInset role="alert">
            <p className="text-sm font-medium text-foreground">This definition has errors.</p>
            <ul className="mt-1.5 space-y-0.5">
              {brokenErrors.map((error, index) => (
                <li key={index} className="text-xs text-destructive-ink">{error}</li>
              ))}
            </ul>
          </CardInset>
        ) : (
          <div className="space-y-4">
            <CardInset padding="none" className="h-64 overflow-hidden" data-testid="workflow-detail-diagram">
              <WorkflowViewerPanel
                projectId={projectId}
                workflowName={workflowRef}
                compact
                hideFullscreen
                hideHeader
                hideLegend
              />
            </CardInset>
            <div>
              <h3 className="mb-1.5 text-xs font-semibold uppercase tracking-wide text-muted-foreground">Inputs</h3>
              {definition.loading ? (
                <p className="text-sm text-muted-foreground">Loading inputs…</p>
              ) : inputs.length === 0 ? (
                <p className="text-sm text-muted-foreground">This workflow takes no inputs beyond the prompt.</p>
              ) : (
                <CardInset padding="none" className="overflow-hidden">
                  <div className="forge-ui" data-testid="workflow-detail-inputs">
                    <DataTable<InputRow>
                      flush
                      compact
                      layout="fixed"
                      ariaLabel="Inputs"
                      columns={INPUT_COLUMNS}
                      data={inputs.map(({ name, schema }) => ({ name, schema }))}
                      getRowKey={(row) => row.name}
                    />
                  </div>
                </CardInset>
              )}
            </div>
          </div>
        )}
      </Card>
    </section>
  );
}

type InputRow = { name: string; schema: InputDef };

const INPUT_COLUMNS: DataTableColumn<InputRow>[] = [
  {
    key: "name",
    header: "Name",
    width: "28%",
    className: "truncate font-mono text-xs",
    render: (_, { name }) => <span title={name}>{name}</span>,
  },
  { key: "type", header: "Type", width: "5rem", className: "text-xs text-muted-foreground", render: (_, { schema }) => schema.type ?? "" },
  {
    key: "default",
    header: "Default",
    width: "24%",
    className: "truncate text-xs",
    render: (_, { schema }) => {
      const fallback = formatInputDefault(getInputDefault(schema));
      return fallback ? (
        <span title={fallback}>{fallback}</span>
      ) : (
        <span className="text-muted-foreground">
          <span aria-hidden="true">—</span>
          <span className="sr-only">None</span>
        </span>
      );
    },
  },
  {
    key: "description",
    header: "Description",
    className: "whitespace-normal py-1.5 align-top text-xs text-muted-foreground",
    render: (_, { schema }) => getInputDescription(schema),
  },
];

function WorkflowNotFound({ workflowRef, projectId, scopeProjectId }: { workflowRef?: string; projectId?: string; scopeProjectId?: string }) {
  return (
    <div className="space-y-4">
    <Card padding="lg" role="alert">
      <p className="text-sm font-medium text-foreground">This workflow no longer exists.</p>
      <p className="mt-1 text-sm text-muted-foreground">
        It may have been deleted or renamed.
        {workflowRef && (
          <>
            {" "}Its past runs are still in{" "}
            <Link
              to="/workflows/runs"
              search={{ workflow: [workflowRef], range: "all" }}
              className="font-medium text-primary hover:underline"
            >
              Runs
            </Link>
            .
          </>
        )}
      </p>
      <Link
        to="/workflows/library"
        className="mt-4 inline-flex h-9 items-center rounded-md border border-border px-3 text-sm font-medium text-foreground hover:bg-muted/60 focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
      >
        Back to workflows
      </Link>
    </Card>
    {/* Triggers can outlive the file they run (a workflow removed from the
        repository): list them, so they can be turned off or removed. */}
    {workflowRef && projectId && (
      <TriggersCard
        workflowRef={workflowRef}
        workflowTitle={workflowRef}
        declared={[]}
        projectId={projectId}
        scopeProjectId={scopeProjectId}
        setupBlockedReason="This workflow no longer exists, so no trigger can be added."
      />
    )}
    </div>
  );
}

function DetailSkeleton() {
  return (
    <div aria-busy="true" aria-label="Loading workflow" className="space-y-6">
      <div className="space-y-2">
        <div className="h-7 w-64 max-w-full animate-pulse rounded bg-border motion-reduce:animate-none" />
        <div className="h-4 w-96 max-w-full animate-pulse rounded bg-border/60 motion-reduce:animate-none" />
      </div>
      {[0, 1, 2].map((card) => (
        <Card key={card} padding="none" className="h-32">
          <div className="h-full animate-pulse bg-border/20 motion-reduce:animate-none" />
        </Card>
      ))}
    </div>
  );
}
