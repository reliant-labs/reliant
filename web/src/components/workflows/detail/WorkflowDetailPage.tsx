// Copyright (c) 2025 Reliant Labs

/**
 * /workflows/library/$workflowRef — one workflow: what it does, how it is
 * used, and Run… (WORKFLOW_UI.md §2.3). Every surface that names a workflow
 * can link here.
 *
 * Main column: recent runs (the Runs tab's own RunList, so the two cannot
 * drift), the automations that run it (the Automations tab's own row), and
 * the definition — its diagram, read-only, and its typed inputs. Side rail:
 * About, this workflow's Presets (decision 7: presets moved here from the
 * old hub, with the global list in Settings → Presets), and Used by: the
 * workflows that call this one through `ref:`.
 *
 * Actions live in the page header and nowhere else: Run… (RunWorkflowDialog),
 * Edit (the builder) and New automation (AutomationFormDialog, prefilled with
 * this workflow). The Definition card used to repeat Edit twice more ("Open in
 * builder", and the diagram's own pencil under a second header); one action,
 * one header.
 */

import { useMemo, useState } from "react";
import { Link, useNavigate, useParams, useSearch } from "@tanstack/react-router";
import { CalendarClock, Pencil, Play } from "lucide-react";

import type { Trigger } from "@/api/trigger-grpc";
import type { WorkflowResponse } from "@/api/workflow-grpc";
import { useRunList } from "@/hooks/run-queries";
import { useTriggers } from "@/hooks/trigger-queries";
import { useWorkflowLibrary } from "@/hooks/workflow-library-queries";
import { formatInputDefault } from "@/lib/inputDefaultDisplay";
import { getInputDefault, getInputDescription } from "@/lib/inputHelpers";
import { WORKFLOWS_LIBRARY_PATH } from "@/lib/workflowsArea";
import { usePreferencesStore } from "@/store/preferencesStore";
import { useProjectStore } from "@/store/projectStore";
import Card, { CardHeader, CardInset } from "../../forge-ui/card";
import PageHeader from "../../forge-ui/page_header";
import { Button } from "../../ui/Button";
import { AutomationRow } from "../../Automations/AutomationRow";
import { AutomationFormDialog } from "../../Automations/AutomationFormDialog";
import { RunList } from "../../runs/RunList";
import { RunWorkflowDialog } from "../../workflow/run/RunWorkflowDialog";
import { WorkflowViewerPanel } from "../../workflow/WorkflowViewerPanel";
import { getWorkflowDisplayName, normalizeWorkflowRef } from "../../workflow/useWorkflowInputs";
import { useWorkflowDefinition } from "../../workflow/useWorkflowDefinition";
import { WorkflowBadge, WorkflowSourceBadge } from "../WorkflowSourceBadge";
import { WorkflowPresetsSection } from "./WorkflowPresetsSection";
import { workflowsUsing } from "./workflowUsedBy";

/** How many runs the Recent runs card shows; "View all" opens the Runs tab. */
export const RECENT_RUNS_LIMIT = 10;

export function WorkflowDetailPage() {
  const { workflowRef: rawRef } = useParams({ strict: false }) as { workflowRef?: string };
  const workflowRef = rawRef ? decodeURIComponent(rawRef) : "";
  const projectId = useProjectStore((state) => state.currentProject?.id);
  const projectsLoading = useProjectStore((state) => state.isLoading);

  if (!workflowRef) return <WorkflowNotFound />;
  if (!projectId) return projectsLoading ? <DetailSkeleton /> : <WorkflowNotFound />;
  return <WorkflowDetail projectId={projectId} workflowRef={workflowRef} />;
}

export function WorkflowDetail({ projectId, workflowRef }: { projectId: string; workflowRef: string }) {
  const navigate = useNavigate();
  const { project } = useSearch({ strict: false }) as { project?: string };
  const library = useWorkflowLibrary(projectId);
  const definition = useWorkflowDefinition(projectId, workflowRef);
  const defaultWorkflow = usePreferencesStore((state) => state.preferences?.defaultWorkflow);
  const [running, setRunning] = useState(false);
  const [creatingAutomation, setCreatingAutomation] = useState(false);

  const listing = library.data?.workflows.find((w) => w.name === workflowRef);
  const invalid = library.data?.invalidWorkflows.find((w) => w.name === workflowRef);

  if (library.isLoading) return <DetailSkeleton />;
  if (library.data && !listing && !invalid) return <WorkflowNotFound workflowRef={workflowRef} />;

  const displayName = getWorkflowDisplayName(workflowRef, true);
  const isDraft = listing?.status === "draft";
  const runBlockedReason = invalid
    ? "This workflow failed to load, so it cannot run."
    : definition.error
      ? "This workflow's definition has errors, so it cannot run."
      : isDraft
        ? "Drafts cannot run until they are marked complete."
        : null;
  const openBuilder = () =>
    void navigate({ to: "/workflow/$workflowName", params: { workflowName: workflowRef } });
  const libraryHref = project ? `${WORKFLOWS_LIBRARY_PATH}?project=${encodeURIComponent(project)}` : WORKFLOWS_LIBRARY_PATH;

  return (
    <div className="space-y-4" data-testid="workflow-detail">
      <div className="forge-ui">
        <PageHeader
          className=""
          breadcrumbs={[{ label: "Library", href: libraryHref }, { label: displayName }]}
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
              label: "New automation",
              variant: "secondary",
              icon: <CalendarClock className="h-4 w-4" aria-hidden="true" />,
              onClick: () => setCreatingAutomation(true),
            },
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

      <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_18rem]">
        <div className="min-w-0 space-y-4">
          <RecentRunsCard
            projectId={projectId}
            workflowRef={workflowRef}
            onRun={runBlockedReason ? undefined : () => setRunning(true)}
            onSchedule={() => setCreatingAutomation(true)}
          />
          <AutomationsCard projectId={projectId} workflowRef={workflowRef} />
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
            </dl>
          </Card>
          <WorkflowPresetsSection projectId={projectId} workflowRef={workflowRef} />
          <UsedByCard workflowRef={workflowRef} workflows={library.data?.workflows ?? []} />
        </aside>
      </div>

      <RunWorkflowDialog open={running} onClose={() => setRunning(false)} projectId={projectId} workflowRef={workflowRef} />
      <AutomationFormDialog
        open={creatingAutomation}
        onClose={() => setCreatingAutomation(false)}
        prefill={{ workflow: workflowRef, projectId, name: displayName }}
        defaultProjectId={projectId}
      />
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
                  {getWorkflowDisplayName(workflow.name, true)}
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
  onSchedule,
}: {
  projectId: string;
  workflowRef: string;
  onRun?: () => void;
  onSchedule: () => void;
}) {
  // Every run of this workflow in this project, whenever it ran and whatever
  // started it — including builder test runs, which are runs of it too.
  const filters = useMemo(
    () => ({ projectId, workflow: [workflowRef], range: "all" as const }),
    [projectId, workflowRef],
  );
  const list = useRunList(filters);
  const runs = list.runs.slice(0, RECENT_RUNS_LIMIT);

  return (
    <section aria-label="Recent runs">
      <Card padding="none" className="overflow-hidden">
        <div className="flex items-center justify-between gap-3 border-b border-border/60 px-4 py-2.5">
          <h2 className="text-sm font-semibold text-foreground">Recent runs</h2>
          <Link
            to="/workflows/runs"
            search={{ workflow: [workflowRef], range: "all" }}
            className="rounded-sm text-xs font-medium text-primary hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
          >
            View all
          </Link>
        </div>
        {list.isLoading ? (
          <p className="px-4 py-3 text-sm text-muted-foreground" aria-busy="true">Loading runs…</p>
        ) : list.isError ? (
          <p className="px-4 py-3 text-sm text-muted-foreground" role="alert">Runs could not be loaded.</p>
        ) : runs.length === 0 ? (
          <div className="flex flex-wrap items-center gap-2 px-4 py-3 text-sm text-muted-foreground">
            <span>Not run yet.</span>
            {onRun && (
              <Button variant="ghost" size="sm" onClick={onRun}>
                Run it now
              </Button>
            )}
            <Button variant="ghost" size="sm" onClick={onSchedule}>
              Add a schedule
            </Button>
          </div>
        ) : (
          <div data-testid="workflow-detail-runs">
            <RunList runs={runs} groupRepeats={false} bare />
          </div>
        )}
      </Card>
    </section>
  );
}

/** The automations that run this workflow. "New automation" is the page header's. */
function AutomationsCard({ projectId, workflowRef }: { projectId: string; workflowRef: string }) {
  const triggers = useTriggers(projectId);
  const usingThis = useMemo<Trigger[]>(
    () =>
      (triggers.data ?? []).filter(
        (trigger) => normalizeWorkflowRef(trigger.workflow) === normalizeWorkflowRef(workflowRef),
      ),
    [triggers.data, workflowRef],
  );

  return (
    <section aria-label="Automations">
      <Card padding="none" className="overflow-hidden">
        <div className="flex items-center justify-between gap-3 border-b border-border/60 px-4 py-2.5">
          <h2 className="text-sm font-semibold text-foreground">Automations</h2>
        </div>
        {triggers.isLoading ? (
          <p className="px-4 py-3 text-sm text-muted-foreground" aria-busy="true">Loading automations…</p>
        ) : usingThis.length === 0 ? (
          <p className="px-4 py-3 text-sm text-muted-foreground">Nothing runs this workflow on its own.</p>
        ) : (
          <ul aria-label="Automations using this workflow" className="divide-y divide-border/60" data-testid="workflow-detail-automations">
            {usingThis.map((trigger) => (
              <AutomationRow key={trigger.id} trigger={trigger} />
            ))}
          </ul>
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
                <li key={index} className="text-xs text-destructive">{error}</li>
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
                  <table className="w-full table-fixed text-xs" aria-label="Inputs" data-testid="workflow-detail-inputs">
                    <thead className="border-b border-border/60 text-left font-semibold uppercase tracking-wider text-muted-foreground">
                      <tr>
                        <th scope="col" className="w-[28%] px-3 py-1.5">Name</th>
                        <th scope="col" className="w-20 px-3 py-1.5">Type</th>
                        <th scope="col" className="w-[24%] px-3 py-1.5">Default</th>
                        <th scope="col" className="px-3 py-1.5">Description</th>
                      </tr>
                    </thead>
                    <tbody className="divide-y divide-border/60">
                      {inputs.map(({ name, schema }) => {
                        const fallback = formatInputDefault(getInputDefault(schema));
                        const description = getInputDescription(schema);
                        return (
                          <tr key={name} className="align-top">
                            <td className="truncate px-3 py-1.5 font-mono text-foreground" title={name}>{name}</td>
                            <td className="px-3 py-1.5 text-muted-foreground">{schema.type ?? ""}</td>
                            <td className="truncate px-3 py-1.5 text-foreground" title={fallback}>
                              {fallback ?? (
                                <span className="text-muted-foreground">
                                  <span aria-hidden="true">—</span>
                                  <span className="sr-only">None</span>
                                </span>
                              )}
                            </td>
                            <td className="px-3 py-1.5 text-muted-foreground">{description}</td>
                          </tr>
                        );
                      })}
                    </tbody>
                  </table>
                </CardInset>
              )}
            </div>
          </div>
        )}
      </Card>
    </section>
  );
}

function WorkflowNotFound({ workflowRef }: { workflowRef?: string }) {
  return (
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
        Back to library
      </Link>
    </Card>
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
