// Copyright (c) 2025 Reliant Labs

/**
 * /workflows/library/$workflowRef — one workflow: what it does, how it is
 * used, and Run now (WORKFLOW_UI.md §2.3). Every surface that names a workflow
 * can link here.
 *
 * It is also the ONE place the workflow is configured to run by itself
 * (automation-ux-spec.md §2.1): the "Turned on" card lists it per project,
 * with Settings, Pause and the state's call to action, and a callout under
 * the header names the worst automation that needs the user.
 *
 * Main column: Turned on, recent runs (the Runs tab's own RunList, so the two
 * cannot drift), and the definition — its diagram, read-only, and its typed
 * inputs. Side rail: About (with what starts it and whether it needs a
 * machine), this workflow's Presets, and Used by: the workflows that call this
 * one through `ref:`.
 *
 * The header holds Run now and Edit only. Turning it on, changing how it runs
 * and adding a custom automation live in Turned on, so the header no longer
 * offers two ways to make the workflow run by itself.
 */

import { useMemo, useState } from "react";
import { Link, useNavigate, useParams, useSearch } from "@tanstack/react-router";
import { Pencil, Play } from "lucide-react";

import type { WorkflowResponse } from "@/api/workflow-grpc";
import { isChatLaunchable } from "@/api/workflow-grpc";
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
import { AutomationFormDialog } from "../../Automations/AutomationFormDialog";
import { AutomationStateNotice } from "../../Automations/AutomationStateNotice";
import { RunList } from "../../runs/RunList";
import { RunNowDialog } from "../../workflow/run/RunNowDialog";
import { WorkflowViewerPanel } from "../../workflow/WorkflowViewerPanel";
import { workflowDisplayName } from "../../../lib/workflowDisplayName";
import { useWorkflowDefinition } from "../../workflow/useWorkflowDefinition";
import { DRAFT_NOT_RUNNABLE } from "../../workflow/workflowDraftStatus";
import { WorkflowBadge, WorkflowSourceBadge } from "../WorkflowSourceBadge";
import { WorkflowPresetsSection } from "./WorkflowPresetsSection";
import { declaredTriggers, describeDeclaredSource } from "@/lib/declaredTriggers";
import { useIntegrationEventNaming } from "@/hooks/connection-queries";
import { workflowAutomationsFor } from "@/lib/automationUnits";
import { describeSchedule } from "@/lib/cronText";
import { TurnedOnCard } from "./TurnedOnCard";
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
  const allTriggers = useTriggers();
  const naming = useIntegrationEventNaming();

  const listing = library.data?.workflows.find((w) => w.name === workflowRef);
  const invalid = library.data?.invalidWorkflows.find((w) => w.name === workflowRef);

  if (library.isLoading) return <DetailSkeleton />;
  if (library.data && !listing && !invalid) return <WorkflowNotFound workflowRef={workflowRef} />;

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
  // The callout names the worst automation of this workflow that needs the user, in any project.
  const needsYou = workflowAutomationsFor(allTriggers.data ?? [], workflowRef).find((unit) => unit.health.needsAttention && unit.health.cta);
  const startsFrom = [
    ...(listing && isChatLaunchable(listing) ? ["Chat"] : []),
    ...declared.map((d) => describeDeclaredSource(d, describeSchedule, naming)),
  ];
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
              label: "Run now",
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
          triggers={needsYou.triggers}
          workflowRef={workflowRef}
          projectId={needsYou.projectId}
          subject={`${displayName} in ${needsYou.projectName ?? "a deleted project"}`}
        />
      )}

      <div className="grid gap-4 lg:grid-cols-[minmax(0,1fr)_18rem]">
        <div className="min-w-0 space-y-4">
          <TurnedOnCard
            workflowRef={workflowRef}
            workflowTitle={displayName}
            declared={declared}
            projectId={projectId}
            setupBlockedReason={setupBlockedReason}
            onAddCustom={() => setCreatingAutomation(true)}
          />
          <RecentRunsCard
            projectId={projectId}
            workflowRef={workflowRef}
            onRun={runBlockedReason ? undefined : () => setRunning(true)}
            onSchedule={() => setCreatingAutomation(true)}
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

      <RunNowDialog open={running} onClose={() => setRunning(false)} projectId={projectId} workflowRef={workflowRef} />
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
