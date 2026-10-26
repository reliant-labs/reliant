// Copyright (c) 2025 Reliant Labs

/**
 * The Workflows tab (/workflows/library, WORKFLOW_UI.md §2.2): every workflow,
 * and — on its row — whether it runs by itself. There is no separate
 * Automations list: an automation is a workflow's triggers, so the row shows
 * what starts it, its health, its last and next run and an On/Off switch, and
 * the workflow's page is where its triggers are configured.
 *
 * ONE table (forge-ui DataTable): a header row and fixed column widths, so
 * every row's cells line up under a label. Rows that need you are pinned on
 * top. Above it, when anything runs by itself in scope, a collapsible summary
 * of what is on: the counts and the next 24 hours (Coming up).
 *
 * Scope is the area's (the header's project menu): one project, or every
 * project — a union of each project's list, since a project's own workflows
 * come from its repository. The search, filter chips and sort are search
 * params (librarySearchSchema), so a narrowed list is a link. What they
 * select is workflowsView.ts.
 */

import { useEffect, useMemo, useRef, useState } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { AlertTriangle, BookOpen, Plus, Upload } from "lucide-react";
import { toast } from "sonner";

import type { InvalidWorkflow, WorkflowResponse } from "@/hooks/workflow-queries";
import { useDebounce } from "@/hooks/useDebounce";
import { useLastRunPerWorkflow } from "@/hooks/run-queries";
import { useTriggers } from "@/hooks/trigger-queries";
import { useIntegrationEventNaming } from "@/hooks/catalog-queries";
import type { Trigger } from "@/hooks/trigger-model";
import { automationCounts, automationUnits, unitsNeedingYou } from "@/lib/automationUnits";
import { describeSchedule, describeTriggerSource } from "@/lib/cronText";
import { describeDeclaredSource, type DeclaredTrigger } from "@/lib/declaredTriggers";
import { formatAbsoluteTime, formatRelativeTime } from "@/lib/relativeTime";
import { WORKFLOWS_LIBRARY_PATH } from "@/lib/workflowsArea";
import { WORKFLOWS_SHOW_KEYS, type LibrarySearch, type LibrarySortKey, type WorkflowsShowKey } from "@/routeSchemas";
import {
  useCopyWorkflow,
  useDeleteWorkflow,
  useImportWorkflow,
  useProjectLibraries,
  useSetWorkflowVisibility,
  useWorkflowLibrary,
} from "@/hooks/workflow-library-queries";
import { useHiddenItems, useScopePreferences, useToggleHiddenItem, useUpdateScopePreferences } from "@/hooks/settings-queries";
import { useProjects } from "@/hooks/project-queries";
import { useProjectStore } from "@/store/projectStore";
import { downloadWorkflow } from "@/hooks/workflow-queries";
import Card from "../../forge-ui/card";
import ConfirmationDialog from "../../forge-ui/confirmation_dialog";
import DataTable from "../../forge-ui/data_table";
import PageHeader from "../../forge-ui/page_header";
import Modal from "../../forge-ui/modal";
import Button from "@/components/forge-ui/button";
import { openAutomationDialog } from "../../Automations/automationDialog";
import { RunNowDialog } from "../../workflow/run/RunNowDialog";
import { workflowDisplayName } from "../../../lib/workflowDisplayName";
import { splitFindings } from "../../workflow/workflowDraftStatus";
import { FilterSearch, SelectFilter } from "../FilterMenu";
import { WORKFLOW_SOURCE_LABEL } from "../WorkflowSourceBadge";
import { useSwitchTriggers } from "../useSwitchTriggers";
import { useWorkflowsScope } from "../useWorkflowsScope";
import { ComingUpTimeline } from "./ComingUpTimeline";
import { workflowsColumns, type WorkflowRowAction, type WorkflowRowItem, type WorkflowsTableRow } from "./WorkflowRow";
import {
  automationsDeletedWith,
  deletedAutomationsSentence,
  workflowsView,
  type ListedWorkflow,
  type WorkflowListRow,
} from "./workflowsView";

type ShowChoice = WorkflowsShowKey | "all";

const SHOW_LABEL: Record<ShowChoice, string> = {
  all: "All",
  on: "On",
  "needs-you": "Needs you",
  builtin: "Built-in",
  yours: "Yours",
  failed: "Failed to load",
};

const SORT_OPTIONS: Array<{ value: LibrarySortKey; label: string }> = [
  { value: "name", label: "Name" },
  { value: "recent", label: "Recently run" },
];

/**
 * The list's search params, and a setter that replaces them. Replace, not
 * push, as on Runs: narrowing the list adjusts a view, and Back should leave
 * the list rather than undo it one keystroke at a time.
 */
function useWorkflowsSearch(): [LibrarySearch, (next: LibrarySearch) => void] {
  const search = useSearch({ strict: false }) as LibrarySearch;
  const navigate = useNavigate();
  const set = (next: LibrarySearch) => {
    const compacted = Object.fromEntries(
      Object.entries(next).filter(([, value]) => value !== undefined && value !== ""),
    ) as LibrarySearch;
    void navigate({ to: WORKFLOWS_LIBRARY_PATH, search: compacted, replace: true });
  };
  return [search, set];
}

export function WorkflowsListPage() {
  const scope = useWorkflowsScope();
  const projectsLoading = useProjects().isLoading;

  return (
    <div className="space-y-4">
      <WorkflowsHeader projectId={scope.currentProjectId} />
      {projectsLoading && !scope.currentProjectId ? <WorkflowsSkeleton /> : <WorkflowsBody />}
    </div>
  );
}

function WorkflowsHeader({ projectId }: { projectId?: string }) {
  const navigate = useNavigate();
  const fileInput = useRef<HTMLInputElement>(null);
  const importWorkflow = useImportWorkflow(projectId ?? "");
  const [conflict, setConflict] = useState<{ slug: string; yaml: string } | null>(null);

  const onFile = async (event: React.ChangeEvent<HTMLInputElement>) => {
    const file = event.target.files?.[0];
    event.target.value = "";
    if (!file || !projectId) return;
    const yaml = await file.text();
    importWorkflow.mutate(
      { yaml, overwrite: false },
      {
        onSuccess: (result) => {
          if (result.conflict) setConflict({ slug: result.slug || "workflow", yaml });
          else if (result.success) toast.success("Imported successfully");
          else toast.error(result.message || "Failed to import");
        },
        onError: (error) => toast.error(error instanceof Error ? error.message : "Failed to import"),
      },
    );
  };

  return (
    <div className="forge-ui">
      <PageHeader
        className=""
        title="Workflows"
        subtitle={
          <>
            What your agents can run, and what runs by itself.{" "}
            <a
              href="https://docs.reliantlabs.io/"
              target="_blank"
              rel="noopener noreferrer"
              className="inline-flex items-center gap-1 text-primary hover:underline"
            >
              <BookOpen className="h-3 w-3" aria-hidden="true" />
              Docs
            </a>
          </>
        }
        actions={
          projectId
            ? [
                {
                  label: "Import",
                  variant: "secondary",
                  icon: <Upload className="h-4 w-4" aria-hidden="true" />,
                  onClick: () => fileInput.current?.click(),
                  disabled: importWorkflow.isPending,
                },
                {
                  label: "New workflow",
                  variant: "primary",
                  icon: <Plus className="h-4 w-4" aria-hidden="true" />,
                  onClick: () => void navigate({ to: "/workflow/new" }),
                },
              ]
            : []
        }
      />
      <input
        ref={fileInput}
        type="file"
        accept=".yaml,.yml"
        onChange={(event) => void onFile(event)}
        className="hidden"
        data-testid="workflow-import-input"
      />
      {conflict && projectId && (
        <ImportConflictModal
          projectId={projectId}
          slug={conflict.slug}
          yaml={conflict.yaml}
          onClose={() => setConflict(null)}
        />
      )}
    </div>
  );
}

/** A workflow by that name exists: replace it, or save under a new name. */
function ImportConflictModal({
  projectId,
  slug,
  yaml,
  onClose,
}: {
  projectId: string;
  slug: string;
  yaml: string;
  onClose: () => void;
}) {
  const importWorkflow = useImportWorkflow(projectId);
  const [newName, setNewName] = useState(() => `${slug}-${Math.random().toString(36).substring(2, 8)}`);

  const replace = () =>
    importWorkflow.mutate(
      { yaml, overwrite: true },
      {
        onSuccess: (result) => {
          if (result.success) {
            toast.success("Replaced successfully");
            onClose();
          } else toast.error(result.message || "Failed to replace");
        },
        onError: (error) => toast.error(error instanceof Error ? error.message : "Failed to replace"),
      },
    );

  const saveAsNew = () => {
    if (!newName.trim()) return;
    importWorkflow.mutate(
      { yaml: yaml.replace(/^name:\s*.+$/m, `name: ${newName.trim()}`), overwrite: false },
      {
        onSuccess: (result) => {
          if (result.success) {
            toast.success("Saved as new workflow");
            onClose();
          } else if (result.conflict) toast.error("Name already exists, try a different name");
          else toast.error(result.message || "Failed to save");
        },
        onError: (error) => toast.error(error instanceof Error ? error.message : "Failed to save"),
      },
    );
  };

  return (
    <Modal open onClose={onClose} title="Workflow already exists" size="md">
      <div className="space-y-4">
        <p className="text-sm text-muted-foreground">
          A workflow named <span className="font-mono font-semibold text-foreground">"{slug}"</span> already exists.
        </p>
        <div className="space-y-2">
          <label htmlFor="workflow-import-new-name" className="block text-sm font-medium text-foreground">
            Save with a new name
          </label>
          <div className="flex gap-2">
            <input
              id="workflow-import-new-name"
              type="text"
              value={newName}
              onChange={(event) => setNewName(event.target.value)}
              onKeyDown={(event) => event.key === "Enter" && saveAsNew()}
              disabled={importWorkflow.isPending}
              className="flex-1 rounded-md border border-border bg-background px-3 py-2 text-sm focus:border-ring focus:outline-none focus:ring-2 focus:ring-ring/20"
            />
            <Button onClick={saveAsNew} disabled={importWorkflow.isPending || !newName.trim()}>
              Save
            </Button>
          </div>
        </div>
        <div className="flex items-center justify-between border-t border-border pt-4">
          <Button variant="outline" onClick={onClose}>
            Cancel
          </Button>
          <Button variant="danger" onClick={replace} disabled={importWorkflow.isPending}>
            Replace existing
          </Button>
        </div>
      </div>
    </Modal>
  );
}

/**
 * What Delete removes, named so it cannot be mistaken for another row with
 * the same title: the slug, where it lives, and whether it was published.
 */
function deleteDescription(workflow: WorkflowResponse, automations: readonly Trigger[]): string {
  const source = WORKFLOW_SOURCE_LABEL[workflow.source] ?? workflow.source;
  const state = workflow.status === "draft" ? "draft" : "published workflow";
  const alsoDeleted = deletedAutomationsSentence(automations);
  return `Deletes the ${state} ${workflow.name} (${source}).${alsoDeleted ? ` ${alsoDeleted}` : ""} This cannot be undone.`;
}

/**
 * The definitions in scope: one project's list, or the union of every
 * project's — built-in and your own workflows once (they are the same in every
 * project), each project's own workflows once per project, named with it.
 */
function useListedWorkflows(projectId: string | undefined, allProjects: boolean) {
  const { projects: listedProjects, isLoading: projectsLoading } = useProjects();
  const currentProject = useProjectStore((state) => state.currentProject);
  // The project list, or at least the current project while it is unknown.
  const projects = useMemo(
    () => (listedProjects.length > 0 ? listedProjects : currentProject ? [{ id: currentProject.id, name: currentProject.name }] : []),
    [listedProjects, currentProject],
  );
  const one = useWorkflowLibrary(allProjects ? undefined : projectId);
  const projectIds = useMemo(() => (allProjects ? projects.map((p) => p.id) : []), [allProjects, projects]);
  const many = useProjectLibraries(projectIds, allProjects);

  return useMemo(() => {
    if (!allProjects) {
      return {
        workflows: (one.data?.workflows ?? []).map((workflow): ListedWorkflow => ({ workflow })),
        invalid: one.data?.invalidWorkflows ?? [],
        isLoading: one.isLoading,
        error: one.isError ? one.error : null,
        complete: !!one.data,
        refetch: () => void one.refetch(),
      };
    }
    const shared = new Map<string, WorkflowResponse>();
    const own: ListedWorkflow[] = [];
    const invalid = new Map<string, InvalidWorkflow>();
    for (const project of projects) {
      const listed = many.byProject.get(project.id);
      if (!listed) continue;
      for (const workflow of listed.workflows) {
        if (workflow.source === "project") own.push({ workflow, projectId: project.id, projectName: project.name });
        else if (!shared.has(workflow.name)) shared.set(workflow.name, workflow);
      }
      for (const broken of listed.invalidWorkflows) invalid.set(`${broken.source}:${broken.path}:${broken.name}`, broken);
    }
    const failed = many.failed.length > 0 && many.byProject.size === 0;
    return {
      workflows: [...[...shared.values()].map((workflow): ListedWorkflow => ({ workflow })), ...own],
      invalid: [...invalid.values()],
      // Until the projects are known there is no list to union: an empty
      // one would read every trigger as an orphan.
      isLoading: projectsLoading || (many.isLoading && many.byProject.size === 0),
      error: failed ? new Error("Workflows could not be loaded in any project.") : null,
      complete: !projectsLoading && many.complete,
      refetch: many.refetch,
    };
  }, [allProjects, one.data, one.isLoading, one.isError, one.error, one.refetch, projects, projectsLoading, many]);
}

/** One line for what starts a workflow, from its triggers in scope or, with none, what it declares. */
function startsOnText(row: WorkflowListRow, naming: ReturnType<typeof useIntegrationEventNaming>): string {
  if (row.triggers.length === 0) {
    const declared = (row.workflow.triggers ?? []) as DeclaredTrigger[];
    if (declared.length === 0) return "";
    return `Off · ${declared.map((d) => describeDeclaredSource(d, describeSchedule, naming)).join("; ")}`;
  }
  const lines = [...new Set(row.triggers.map((t) => describeTriggerSource(t.source, naming)))];
  if (row.triggers.length === 1) return lines[0]!;
  return lines.length === 1 ? `${lines[0]} · ${row.triggers.length} triggers` : `${row.triggers.length} triggers`;
}

function WorkflowsBody() {
  const navigate = useNavigate();
  const scope = useWorkflowsScope();
  const [search, setSearch] = useWorkflowsSearch();
  const listed = useListedWorkflows(scope.currentProjectId, scope.allProjects);
  const lastRuns = useLastRunPerWorkflow(scope.projectId, scope.allProjects);
  const triggers = useTriggers(scope.projectId);
  const naming = useIntegrationEventNaming();
  const { switchTriggers, isPending: switching } = useSwitchTriggers();
  // Run… and the definition actions work in a project: the row's own, or the current one.
  const [running, setRunning] = useState<{ ref: string; projectId: string } | null>(null);
  // The workflow whose Delete is being confirmed.
  const [deleting, setDeleting] = useState<{ workflow: WorkflowResponse; projectId: string } | null>(null);

  const actionProject = scope.currentProjectId ?? "";
  const deleteWorkflow = useDeleteWorkflow(deleting?.projectId ?? actionProject);
  // Deleting a workflow deletes the triggers that run it, in every project.
  const allTriggers = useTriggers(undefined, { enabled: deleting !== null });
  const deletedAutomations = useMemo(() => {
    if (!deleting) return [];
    return automationsDeletedWith(deleting.workflow, allTriggers.data ?? triggers.data ?? []);
  }, [deleting, allTriggers.data, triggers.data]);
  const copyWorkflow = useCopyWorkflow(actionProject);
  const setVisibility = useSetWorkflowVisibility(actionProject);
  const scopePreferences = useScopePreferences();
  // Unknown until the answer arrives: no "default" badge rather than a guess.
  const defaultWorkflow = scopePreferences.isPending ? undefined : scopePreferences.data.defaultWorkflow;
  const { mutateAsync: updatePreferences } = useUpdateScopePreferences();
  const { isWorkflowHidden } = useHiddenItems();
  const { toggleWorkflowVisibility } = useToggleHiddenItem();

  const triggerList = useMemo(() => triggers.data ?? [], [triggers.data]);
  const view = useMemo(
    () =>
      workflowsView({
        workflows: listed.workflows,
        invalid: listed.invalid,
        triggers: triggerList,
        lastRuns: lastRuns.data,
        complete: listed.complete && !triggers.isLoading,
        q: search.q,
        show: search.show,
        sort: search.sort,
      }),
    [listed.workflows, listed.invalid, listed.complete, triggerList, triggers.isLoading, lastRuns.data, search.q, search.show, search.sort],
  );

  if (listed.isLoading) return <WorkflowsSkeleton />;
  if (listed.error) {
    return (
      <Card padding="lg" role="alert">
        <p className="text-sm font-medium text-foreground">Workflows could not be loaded.</p>
        <p className="mt-1 text-sm text-muted-foreground">
          {listed.error instanceof Error ? listed.error.message : String(listed.error)}
        </p>
        <Button className="mt-4" variant="outline" onClick={listed.refetch}>
          Try again
        </Button>
      </Card>
    );
  }

  const isHidden = (workflow: WorkflowResponse) =>
    workflow.source === "user" ? !!workflow.isHidden : isWorkflowHidden(workflow.name);

  const toItem = (workflow: WorkflowResponse): WorkflowRowItem => ({
    name: workflow.name,
    title: workflow.title,
    description: workflow.description,
    source: workflow.source,
    isDraft: workflow.status === "draft",
    draftErrorCount: splitFindings(workflow.validationErrors ?? []).errors.length,
    isHidden: isHidden(workflow),
    isDefault: defaultWorkflow === workflow.name,
  });

  const actionsFor = (row: WorkflowListRow, projectId: string): WorkflowRowAction[] => {
    const { workflow } = row;
    const displayName = workflowDisplayName(workflow);
    const actions: WorkflowRowAction[] = [];
    if (!row.missing && workflow.status !== "draft" && projectId) {
      // The one way to make it run by itself, whether or not it declares
      // triggers of its own: the dialog offers both.
      actions.push({ label: "Add trigger…", onSelect: () => openAutomationDialog({ workflowRef: workflow.name, projectId, custom: true }) });
    }
    if (row.missing) return actions;
    actions.push({
      label: "Edit",
      onSelect: () => void navigate({ to: "/workflow/$workflowName", params: { workflowName: workflow.name } }),
    });
    if (!projectId) return actions;
    actions.push({
      label: "Duplicate",
      onSelect: () =>
        copyWorkflow.mutate(workflow.name, {
          onSuccess: (result) => {
            if (!result.success) {
              toast.error(result.message || "Failed to copy workflow");
              return;
            }
            toast.success(`Created "${result.slug}" from "${displayName}"`);
            // The copy is for editing, so open it.
            if (result.slug) {
              void navigate({ to: "/workflow/$workflowName", params: { workflowName: result.slug } });
            }
          },
          onError: (error) => toast.error(error instanceof Error ? error.message : "Failed to copy workflow"),
        }),
    });
    // A default must be runnable from a chat, so neither a draft nor a
    // workflow whose Chat trigger is off can be one.
    if (workflow.status !== "draft" && !workflow.automationOnly && defaultWorkflow !== workflow.name) {
      actions.push({
        label: "Set as default",
        onSelect: () =>
          void updatePreferences({ defaultWorkflow: workflow.name })
            .then(() => toast.success(`Set "${displayName}" as default`))
            .catch(() => toast.error("Failed to set default")),
      });
    }
    const hidden = isHidden(workflow);
    actions.push({
      label: hidden ? "Show in composer" : "Hide from composer",
      onSelect: () => {
        // A user workflow's visibility is stored on the workflow; a built-in
        // or project one, in the user's preferences.
        const done = () =>
          toast.success(hidden ? `"${displayName}" visible in dropdown` : `"${displayName}" hidden from dropdown`);
        const failed = (error: unknown) =>
          toast.error(error instanceof Error ? error.message : "Failed to update visibility");
        if (workflow.source === "user") {
          setVisibility.mutate({ filename: workflow.filename, hidden: !hidden }, { onSuccess: done, onError: failed });
        } else {
          void toggleWorkflowVisibility(workflow.name).then(done, failed);
        }
      },
    });
    if (workflow.source === "user") {
      actions.push({
        label: "Export",
        onSelect: () =>
          void downloadWorkflow(projectId, workflow.filename)
            .then(() => toast.success("Exported successfully"))
            .catch((error: unknown) => toast.error(error instanceof Error ? error.message : "Failed to export")),
      });
      actions.push({
        label: "Delete",
        destructive: true,
        onSelect: () => setDeleting({ workflow, projectId }),
      });
    }
    return actions;
  };

  const toTableRow = (row: WorkflowListRow): WorkflowsTableRow => {
    const { workflow } = row;
    const projectId = row.projectId ?? scope.currentProjectId ?? "";
    const projects = new Set(row.triggers.map((t) => t.projectId));
    const needing = unitsNeedingYou(row.units).find((unit) => unit.health.cta);
    const runnable = !row.missing && workflow.status !== "draft" && !!projectId;
    return {
      key: row.key,
      workflow: toItem(workflow),
      view: row,
      startsOn: startsOnText(row, naming),
      lastRun: lastRuns.data?.get(workflow.name),
      href: { workflowRef: workflow.name, project: row.projectId ?? search.project },
      onRun: runnable ? () => setRunning({ ref: workflow.name, projectId }) : undefined,
      onSetUp:
        row.declaresTriggers && row.triggers.length === 0 && runnable
          ? () => openAutomationDialog({ workflowRef: workflow.name, projectId })
          : undefined,
      // One switch per row while its triggers live in one project; across
      // several, each project is switched on the workflow's page.
      onToggle:
        row.triggers.length > 0 && projects.size === 1
          ? (next) => switchTriggers(row.triggers, next, workflowDisplayName(workflow))
          : undefined,
      toggling: switching,
      cta: needing?.health.cta
        ? { spec: needing.health.cta, triggers: needing.kind === "workflow" ? needing.triggers : [needing.trigger], projectId: needing.kind === "workflow" ? needing.projectId : needing.trigger.projectId }
        : undefined,
      actions: actionsFor(row, projectId),
    };
  };
  const tableRows = view.rows.map(toTableRow);
  const needingCount = view.rows.filter((row) => row.needsYou).length;
  const clearNarrowing = () => setSearch({ ...search, q: undefined, show: undefined });

  return (
    <div className="space-y-3" data-onboarding="workflow-hub">
      {triggerList.length > 0 && <OnSummary triggers={triggerList} allProjects={scope.allProjects} />}

      <WorkflowsControls search={search} onChange={setSearch} showFailed={listed.invalid.length > 0} />

      {needingCount > 0 && (
        <p className="flex items-center gap-1.5 text-xs text-warning-ink" role="status">
          <AlertTriangle className="h-3.5 w-3.5" aria-hidden="true" />
          {needingCount === 1 ? "1 workflow needs" : `${needingCount} workflows need`} you: a trigger is broken, not set up,
          or keeps failing. Pinned to the top.
        </p>
      )}

      {view.noMatches ? (
        <Card padding="lg" role="status">
          <p className="text-sm font-medium text-foreground">
            {search.q?.trim()
              ? `No workflows match “${search.q.trim()}”.`
              : search.show === "on"
                ? "Nothing runs by itself here yet."
                : "No workflows here."}
          </p>
          <Button className="mt-4" variant="outline" size="sm" onClick={clearNarrowing}>
            {search.q?.trim() ? "Clear search" : "Show all workflows"}
          </Button>
        </Card>
      ) : (
        <>
          {tableRows.length > 0 && (
            <div className="forge-ui">
              <DataTable<WorkflowsTableRow>
                ariaLabel="Workflows"
                columns={workflowsColumns()}
                data={tableRows}
                layout="fixed"
                compact
                getRowKey={(row) => row.key}
                getRowProps={(row) => ({
                  "data-testid": `workflow-row-${row.workflow.name}${row.view.projectId ? `-${row.view.projectId}` : ""}`,
                  "data-attention": row.view.needsYou ? "true" : undefined,
                  "data-state": row.view.state,
                  className: row.view.needsYou ? "bg-warning/5" : undefined,
                })}
              />
            </div>
          )}
          {view.noWorkflowsOfYourOwn && (
            <p className="px-1 text-sm text-muted-foreground">
              None of your own yet. Start from a built-in, or{" "}
              <button
                type="button"
                onClick={() => void navigate({ to: "/workflow/new" })}
                className="font-medium text-primary hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
              >
                create your own
              </button>
              .
            </p>
          )}
          {view.buildingBlocks.length > 0 && (
            <details className="group" data-testid="building-blocks">
              <summary className="mb-2 cursor-pointer select-none px-1 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
                Building blocks
                <span className="ml-1.5 font-medium text-muted-foreground/70">{view.buildingBlocks.length}</span>
              </summary>
              <p className="mb-2 px-1 text-xs text-muted-foreground">
                Used by other workflows. You can run them directly, but they are not offered in the composer.
              </p>
              <div className="forge-ui">
                <DataTable<WorkflowsTableRow>
                  ariaLabel="Building blocks"
                  columns={workflowsColumns()}
                  data={view.buildingBlocks.map(toTableRow)}
                  layout="fixed"
                  compact
                  getRowKey={(row) => row.key}
                  getRowProps={(row) => ({ "data-testid": `workflow-row-${row.workflow.name}` })}
                />
              </div>
            </details>
          )}
          {view.invalid.length > 0 && <InvalidSection workflows={view.invalid} />}
        </>
      )}

      <ConfirmationDialog
        open={deleting !== null}
        title={deleting ? `Delete “${workflowDisplayName(deleting.workflow)}”?` : ""}
        description={deleting ? deleteDescription(deleting.workflow, deletedAutomations) : undefined}
        confirmLabel="Delete workflow"
        loading={deleteWorkflow.isPending}
        onCancel={() => setDeleting(null)}
        onConfirm={() => {
          if (!deleting) return;
          const displayName = workflowDisplayName(deleting.workflow);
          deleteWorkflow.mutate(deleting.workflow.filename, {
            onSuccess: () => {
              setDeleting(null);
              toast.success(`Deleted “${displayName}”`);
            },
            onError: (error) => toast.error(error instanceof Error ? error.message : "Failed to delete"),
          });
        }}
      />

      <RunNowDialog
        open={running !== null}
        onClose={() => setRunning(null)}
        projectId={running?.projectId ?? actionProject}
        workflowRef={running?.ref ?? ""}
      />
    </div>
  );
}

/**
 * What runs by itself in scope, at a glance: how many need you, are on and
 * healthy, are off, and when the next run is; then the next 24 hours.
 * Collapsible — it summarises the table below, it does not replace it.
 */
function OnSummary({ triggers, allProjects }: { triggers: Trigger[]; allProjects: boolean }) {
  const counts = useMemo(() => automationCounts(automationUnits(triggers)), [triggers]);
  const cells: Array<{ label: string; value: string; title?: string; tone?: string }> = [
    { label: "need you", value: String(counts.needYou), tone: counts.needYou > 0 ? "text-warning-ink" : undefined },
    { label: "on and healthy", value: String(counts.healthyOn) },
    { label: "off", value: String(counts.paused) },
    {
      label: "until the next scheduled run",
      value: counts.nextRunAt ? formatRelativeTime(counts.nextRunAt) : "—",
      title: counts.nextRunAt ? formatAbsoluteTime(counts.nextRunAt) : "Nothing is scheduled",
    },
  ];
  return (
    <details open className="group space-y-3" data-testid="on-summary">
      <summary className="cursor-pointer select-none px-1 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
        Runs by itself{allProjects ? ", every project" : ""}
      </summary>
      <Card padding="none" className="overflow-hidden" data-testid="automation-counts">
        <dl className="grid grid-cols-2 divide-border/60 sm:grid-cols-4 sm:divide-x">
          {cells.map((cell) => (
            <div key={cell.label} className="flex flex-col-reverse px-4 py-3" title={cell.title}>
              <dd className={`text-2xl font-semibold tabular-nums text-foreground ${cell.tone ?? ""}`}>{cell.value}</dd>
              <dt className="mt-0.5 text-xs text-muted-foreground">{cell.label}</dt>
            </div>
          ))}
        </dl>
      </Card>
      <ComingUpTimeline triggers={triggers} />
    </details>
  );
}

/**
 * Search, the filter chips and sort: one compact row. The chips are the
 * list's main narrowing (All · On · Needs you · Built-in · Yours), so they
 * are visible buttons, not a menu.
 */
function WorkflowsControls({
  search,
  onChange,
  showFailed,
}: {
  search: LibrarySearch;
  onChange: (next: LibrarySearch) => void;
  showFailed: boolean;
}) {
  // The box writes to the URL once typing pauses, not per keystroke.
  const [query, setQuery] = useState(search.q ?? "");
  const debouncedQuery = useDebounce(query, 200);
  useEffect(() => {
    if ((search.q ?? "") !== debouncedQuery.trim()) onChange({ ...search, q: debouncedQuery.trim() || undefined });
    // eslint-disable-next-line react-hooks/exhaustive-deps -- only the debounced text drives this
  }, [debouncedQuery]);
  useEffect(() => {
    // An outside change (Clear search, Back) resets the box.
    if ((search.q ?? "") !== query.trim()) setQuery(search.q ?? "");
    // eslint-disable-next-line react-hooks/exhaustive-deps -- only the URL drives this
  }, [search.q]);

  const chips: ShowChoice[] = ["all", ...WORKFLOWS_SHOW_KEYS.filter((key) => key !== "failed" || showFailed || search.show === "failed")];
  const active: ShowChoice = search.show ?? "all";

  return (
    <div className="flex flex-wrap items-center gap-2" role="group" aria-label="Find workflows">
      <FilterSearch value={query} onChange={setQuery} placeholder="Search names and descriptions" label="Search workflows" />
      <div className="flex flex-wrap items-center gap-1" role="group" aria-label="Show">
        {chips.map((chip) => (
          <button
            key={chip}
            type="button"
            aria-pressed={active === chip}
            onClick={() => onChange({ ...search, show: chip === "all" ? undefined : chip })}
            className={
              active === chip
                ? "inline-flex h-7 items-center rounded-full border border-primary/40 bg-primary/10 px-2.5 text-xs font-medium text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
                : "inline-flex h-7 items-center rounded-full border border-border px-2.5 text-xs font-medium text-muted-foreground transition-colors hover:text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
            }
          >
            {SHOW_LABEL[chip]}
          </button>
        ))}
      </div>
      <SelectFilter<LibrarySortKey>
        label="Sort"
        options={SORT_OPTIONS}
        value={search.sort ?? "name"}
        defaultValue="name"
        onChange={(sort) => onChange({ ...search, sort: sort === "name" ? undefined : sort })}
      />
    </div>
  );
}

/**
 * Definitions that did not parse. They cannot run or be opened in the builder
 * as a graph, so each shows its file and errors, and how to fix it.
 */
function InvalidSection({ workflows }: { workflows: InvalidWorkflow[] }) {
  return (
    <section aria-label="Failed to load" className="pt-2">
      <h2 className="mb-1.5 flex items-center gap-1.5 px-1 text-xs font-semibold uppercase tracking-wide text-destructive-ink">
        <AlertTriangle className="h-3.5 w-3.5" aria-hidden="true" />
        Failed to load
        <span className="ml-0.5 font-medium text-destructive-ink/70">{workflows.length}</span>
      </h2>
      <Card padding="none" className="border-destructive/40">
        <ul aria-label="Failed to load" className="divide-y divide-border/60">
          {workflows.map((workflow) => (
            <li key={`${workflow.source}-${workflow.name}`} className="px-4 py-2.5" data-testid={`invalid-workflow-${workflow.name}`}>
              <p className="text-sm font-medium text-foreground">{workflowDisplayName(workflow)}</p>
              <p className="mt-0.5 truncate font-mono text-xs text-muted-foreground" title={workflow.path}>
                {workflow.path}
              </p>
              <ul className="mt-1.5 space-y-0.5">
                {workflow.errors.map((error, index) => (
                  <li key={index} className="text-xs text-destructive-ink">
                    {typeof error === "string" ? error : JSON.stringify(error)}
                  </li>
                ))}
              </ul>
              <p className="mt-1.5 text-xs text-muted-foreground">
                Fix the YAML file directly, or ask the workflow assistant to resolve it.
              </p>
            </li>
          ))}
        </ul>
      </Card>
    </section>
  );
}

function WorkflowsSkeleton() {
  return (
    <Card padding="none" aria-busy="true" aria-label="Loading workflows">
      {Array.from({ length: 6 }, (_, row) => (
        <div key={row} className="flex items-center gap-4 border-b border-border/60 px-4 py-2.5 last:border-b-0">
          <div className="flex-1 space-y-1.5">
            <div className="h-3.5 w-48 max-w-full animate-pulse rounded bg-border motion-reduce:animate-none" />
            <div className="h-3 w-72 max-w-full animate-pulse rounded bg-border/60 motion-reduce:animate-none" />
          </div>
          <div className="hidden h-3 w-24 animate-pulse rounded bg-border/60 motion-reduce:animate-none md:block" />
          <div className="h-8 w-16 animate-pulse rounded-md bg-border/60 motion-reduce:animate-none" />
        </div>
      ))}
    </Card>
  );
}
