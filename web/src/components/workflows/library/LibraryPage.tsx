// Copyright (c) 2025 Reliant Labs

/**
 * The Library tab (/workflows/library, WORKFLOW_UI.md §2.2): find a workflow
 * and see whether it is in use. It replaces the old hub's Workflows tab.
 *
 * Rows, not a card grid: a row reads faster and has room for the two facts
 * the hub never had — how many automations run a workflow, and how its last
 * run went (LastRunPerWorkflow, one request for the whole list). The hub's
 * sections are kept: Custom, Built-in, and "Failed to load" for definitions
 * that did not parse. Clicking a workflow opens its detail page.
 */

import { useMemo, useRef, useState } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { AlertTriangle, BookOpen, FolderOpen, Plus, Upload } from "lucide-react";
import { toast } from "sonner";

import type { InvalidWorkflow, WorkflowResponse } from "@/api/workflow-grpc";
import { useLastRunPerWorkflow } from "@/hooks/run-queries";
import { useTriggers } from "@/hooks/trigger-queries";
import {
  useCopyWorkflow,
  useDeleteWorkflow,
  useImportWorkflow,
  useSetWorkflowVisibility,
  useWorkflowLibrary,
} from "@/hooks/workflow-library-queries";
import { usePreferencesStore } from "@/store/preferencesStore";
import { useProjectStore } from "@/store/projectStore";
import { workflowGrpc } from "@/api/workflow-grpc";
import Card from "../../forge-ui/card";
import { Button } from "../../ui/Button";
import { Modal } from "../../ui/Modal";
import { RunWorkflowDialog } from "../../workflow/run/RunWorkflowDialog";
import { getWorkflowDisplayName, normalizeWorkflowRef } from "../../workflow/useWorkflowInputs";
import { splitFindings } from "../../workflow/workflowDraftStatus";
import { WorkflowRow, type WorkflowRowAction, type WorkflowRowItem } from "./WorkflowRow";

export function LibraryPage() {
  const currentProject = useProjectStore((state) => state.currentProject);
  const projectId = currentProject?.id;
  const projectsLoading = useProjectStore((state) => state.isLoading);

  return (
    <div className="space-y-5">
      <LibraryHeader projectId={projectId} />
      {projectId ? (
        <LibraryBody projectId={projectId} />
      ) : projectsLoading ? (
        <LibrarySkeleton />
      ) : (
        <NoProject />
      )}
    </div>
  );
}

function LibraryHeader({ projectId }: { projectId?: string }) {
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
    <div className="flex flex-wrap items-end justify-between gap-4">
      <div className="min-w-0">
        <h1 className="text-2xl font-semibold tracking-tight text-foreground">Workflows</h1>
        <p className="mt-1 max-w-2xl text-sm text-muted-foreground">
          What your agents can run.{" "}
          <a
            href="https://docs.reliantlabs.io/"
            target="_blank"
            rel="noopener noreferrer"
            className="inline-flex items-center gap-1 text-primary hover:underline"
          >
            <BookOpen className="h-3 w-3" aria-hidden="true" />
            Docs
          </a>
        </p>
      </div>
      {projectId && (
        <div className="flex items-center gap-2">
          <Button
            variant="outline"
            size="sm"
            onClick={() => fileInput.current?.click()}
            disabled={importWorkflow.isPending}
            leftIcon={<Upload className="h-4 w-4" />}
          >
            Import
          </Button>
          <input
            ref={fileInput}
            type="file"
            accept=".yaml,.yml"
            onChange={(event) => void onFile(event)}
            className="hidden"
            data-testid="workflow-import-input"
          />
          <Button
            variant="primary"
            size="sm"
            onClick={() => void navigate({ to: "/workflow/new" })}
            leftIcon={<Plus className="h-4 w-4" />}
          >
            New workflow
          </Button>
        </div>
      )}
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
    <Modal isOpen onClose={onClose} title="Workflow already exists" size="md">
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
          <Button variant="destructive" onClick={replace} disabled={importWorkflow.isPending}>
            Replace existing
          </Button>
        </div>
      </div>
    </Modal>
  );
}

function LibraryBody({ projectId }: { projectId: string }) {
  const navigate = useNavigate();
  const search = useSearch({ strict: false }) as { project?: string };
  const library = useWorkflowLibrary(projectId);
  const lastRuns = useLastRunPerWorkflow(projectId);
  const triggers = useTriggers(projectId);
  const [runRef, setRunRef] = useState<string | null>(null);

  const deleteWorkflow = useDeleteWorkflow(projectId);
  const copyWorkflow = useCopyWorkflow(projectId);
  const setVisibility = useSetWorkflowVisibility(projectId);
  const defaultWorkflow = usePreferencesStore((state) => state.preferences?.defaultWorkflow);
  const updatePreferences = usePreferencesStore((state) => state.updatePreferences);
  const isWorkflowHidden = usePreferencesStore((state) => state.isWorkflowHidden);
  const toggleWorkflowVisibility = usePreferencesStore((state) => state.toggleWorkflowVisibility);

  const automationCounts = useMemo(() => {
    const counts = new Map<string, number>();
    for (const trigger of triggers.data ?? []) {
      const key = normalizeWorkflowRef(trigger.workflow);
      counts.set(key, (counts.get(key) ?? 0) + 1);
    }
    return counts;
  }, [triggers.data]);

  const { custom, builtin } = useMemo(() => {
    const workflows = library.data?.workflows ?? [];
    const byName = (a: WorkflowResponse, b: WorkflowResponse) =>
      normalizeWorkflowRef(a.name).localeCompare(normalizeWorkflowRef(b.name));
    return {
      custom: workflows.filter((w) => w.source !== "builtin").sort(byName),
      builtin: workflows.filter((w) => w.source === "builtin").sort(byName),
    };
  }, [library.data]);

  if (library.isLoading) return <LibrarySkeleton />;
  if (library.isError) {
    return (
      <Card padding="lg" role="alert">
        <p className="text-sm font-medium text-foreground">Workflows could not be loaded.</p>
        <p className="mt-1 text-sm text-muted-foreground">
          {library.error instanceof Error ? library.error.message : String(library.error)}
        </p>
        <Button className="mt-4" variant="outline" onClick={() => void library.refetch()}>
          Try again
        </Button>
      </Card>
    );
  }

  const isHidden = (workflow: WorkflowResponse) =>
    workflow.source === "user" ? !!workflow.isHidden : isWorkflowHidden(workflow.name);

  const toItem = (workflow: WorkflowResponse): WorkflowRowItem => ({
    name: workflow.name,
    description: workflow.description,
    source: workflow.source,
    isDraft: workflow.status === "draft",
    draftErrorCount: splitFindings(workflow.validationErrors ?? []).errors.length,
    isHidden: isHidden(workflow),
    isDefault: defaultWorkflow === workflow.name,
  });

  const actionsFor = (workflow: WorkflowResponse): WorkflowRowAction[] => {
    const displayName = normalizeWorkflowRef(workflow.name);
    const actions: WorkflowRowAction[] = [
      {
        label: "Edit",
        onSelect: () =>
          void navigate({ to: "/workflow/$workflowName", params: { workflowName: workflow.name } }),
      },
      {
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
      },
    ];
    // A default must be runnable, so a draft cannot be one.
    if (workflow.status !== "draft" && defaultWorkflow !== workflow.name) {
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
          void workflowGrpc
            .downloadWorkflow(projectId, workflow.filename)
            .then(() => toast.success("Exported successfully"))
            .catch((error: unknown) => toast.error(error instanceof Error ? error.message : "Failed to export")),
      });
      actions.push({
        label: "Delete",
        destructive: true,
        onSelect: () => {
          if (!window.confirm(`Delete "${displayName}"? This cannot be undone.`)) return;
          deleteWorkflow.mutate(workflow.filename, {
            onSuccess: () => toast.success(`Deleted "${displayName}"`),
            onError: (error) => toast.error(error instanceof Error ? error.message : "Failed to delete"),
          });
        },
      });
    }
    return actions;
  };

  const renderRows = (workflows: WorkflowResponse[]) =>
    workflows.map((workflow) => (
      <WorkflowRow
        key={workflow.name}
        workflow={toItem(workflow)}
        lastRun={lastRuns.data?.get(workflow.name)}
        automationCount={automationCounts.get(normalizeWorkflowRef(workflow.name)) ?? 0}
        project={search.project}
        onRun={workflow.status === "draft" ? undefined : () => setRunRef(workflow.name)}
        actions={actionsFor(workflow)}
      />
    ));

  const invalid = library.data?.invalidWorkflows ?? [];

  return (
    <div className="space-y-6" data-onboarding="workflow-hub">
      <LibrarySection
        label="Your workflows"
        count={custom.length}
        empty={
          <p className="px-5 py-4 text-sm text-muted-foreground">
            Start from a built-in, or{" "}
            <button
              type="button"
              onClick={() => void navigate({ to: "/workflow/new" })}
              className="font-medium text-primary hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
            >
              create your own
            </button>
            .
          </p>
        }
      >
        {renderRows(custom)}
      </LibrarySection>

      {builtin.length > 0 && (
        <LibrarySection label="Built-in" count={builtin.length}>
          {renderRows(builtin)}
        </LibrarySection>
      )}

      {invalid.length > 0 && <InvalidSection workflows={invalid} />}

      <RunWorkflowDialog
        open={runRef !== null}
        onClose={() => setRunRef(null)}
        projectId={projectId}
        workflowRef={runRef ?? ""}
      />
    </div>
  );
}

function LibrarySection({
  label,
  count,
  empty,
  children,
}: {
  label: string;
  count: number;
  empty?: React.ReactNode;
  children: React.ReactNode;
}) {
  return (
    <section aria-label={label}>
      <h2 className="mb-2 px-1 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
        {label}
        <span className="ml-1.5 font-medium text-muted-foreground/70">{count}</span>
      </h2>
      <Card padding="none">
        {count === 0 && empty ? (
          empty
        ) : (
          <ul aria-label={label} className="divide-y divide-border/60">
            {children}
          </ul>
        )}
      </Card>
    </section>
  );
}

/**
 * Definitions that did not parse. They cannot run or be opened in the builder
 * as a graph, so each shows its file and errors, and how to fix it.
 */
function InvalidSection({ workflows }: { workflows: InvalidWorkflow[] }) {
  return (
    <section aria-label="Failed to load">
      <h2 className="mb-2 flex items-center gap-1.5 px-1 text-xs font-semibold uppercase tracking-wide text-destructive">
        <AlertTriangle className="h-3.5 w-3.5" aria-hidden="true" />
        Failed to load
        <span className="ml-0.5 font-medium text-destructive/70">{workflows.length}</span>
      </h2>
      <Card padding="none" className="border-destructive/40">
        <ul aria-label="Failed to load" className="divide-y divide-border/60">
          {workflows.map((workflow) => (
            <li key={`${workflow.source}-${workflow.name}`} className="px-5 py-3.5" data-testid={`invalid-workflow-${workflow.name}`}>
              <p className="text-sm font-medium text-foreground">{getWorkflowDisplayName(workflow.name, true)}</p>
              <p className="mt-0.5 truncate font-mono text-xs text-muted-foreground" title={workflow.path}>
                {workflow.path}
              </p>
              <ul className="mt-1.5 space-y-0.5">
                {workflow.errors.map((error, index) => (
                  <li key={index} className="text-xs text-destructive">
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

function NoProject() {
  const navigate = useNavigate();
  return (
    <Card padding="lg" className="flex flex-col items-center px-6 py-12 text-center">
      <div className="flex h-12 w-12 items-center justify-center rounded-full border border-border/60 bg-background text-muted-foreground">
        <FolderOpen className="h-6 w-6" aria-hidden="true" />
      </div>
      <h2 className="mt-4 text-base font-semibold text-foreground">Workflows run inside a project</h2>
      <p className="mt-2 max-w-md text-sm text-muted-foreground">
        Pick a project to see its workflows. Runs and Automations list every project.
      </p>
      <Button className="mt-6" variant="outline" onClick={() => void navigate({ to: "/", search: {} })}>
        Choose a project
      </Button>
    </Card>
  );
}

function LibrarySkeleton() {
  return (
    <Card padding="none" aria-busy="true" aria-label="Loading workflows">
      {Array.from({ length: 5 }, (_, row) => (
        <div key={row} className="flex items-center gap-4 border-b border-border/60 px-5 py-4 last:border-b-0">
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
