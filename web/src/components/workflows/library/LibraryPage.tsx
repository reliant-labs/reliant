// Copyright (c) 2025 Reliant Labs

/**
 * The Library (/workflows/library, WORKFLOW_UI.md §2.2): find a workflow and
 * see whether it is in use. It replaces the old hub's Workflows tab.
 *
 * ONE table, not a card per section: a header row and fixed column widths
 * (forge-ui DataTable), so every row's Automations and Last run line up under
 * a label — the two facts the hub never had (LastRunPerWorkflow, one request
 * for the whole list). The hub's sections survive as the Source column and
 * filter; "Needs attention" rows are pinned on top and flagged; definitions
 * that did not parse get their own alert below, since they have no columns.
 *
 * Above the table: a search, a Source menu and a Sort menu, all search params
 * (librarySearchSchema) so a narrowed library is a link. What they select is
 * libraryView.ts.
 */

import { useEffect, useMemo, useRef, useState } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";
import { AlertTriangle, BookOpen, FolderOpen, Plus, Upload } from "lucide-react";
import { toast } from "sonner";

import type { InvalidWorkflow, WorkflowResponse } from "@/api/workflow-grpc";
import { useDebounce } from "@/hooks/useDebounce";
import { useLastRunPerWorkflow } from "@/hooks/run-queries";
import { useTriggers } from "@/hooks/trigger-queries";
import { WORKFLOWS_LIBRARY_PATH } from "@/lib/workflowsArea";
import type { LibrarySearch, LibrarySortKey, LibrarySourceKey } from "@/routeSchemas";
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
import DataTable from "../../forge-ui/data_table";
import EmptyState from "../../forge-ui/empty_state";
import PageHeader from "../../forge-ui/page_header";
import { Button } from "../../ui/Button";
import { Modal } from "../../ui/Modal";
import { RunWorkflowDialog } from "../../workflow/run/RunWorkflowDialog";
import { getWorkflowDisplayName, normalizeWorkflowRef } from "../../workflow/useWorkflowInputs";
import { splitFindings } from "../../workflow/workflowDraftStatus";
import { FilterSearch, SelectFilter } from "../FilterMenu";
import { libraryColumns, type LibraryTableRow, type WorkflowRowAction, type WorkflowRowItem } from "./WorkflowRow";
import { libraryView } from "./libraryView";

type SourceChoice = LibrarySourceKey | "all";

const SOURCE_OPTIONS: Array<{ value: SourceChoice; label: string }> = [
  { value: "all", label: "All" },
  { value: "yours", label: "Yours & project" },
  { value: "builtin", label: "Built-in" },
  { value: "failed", label: "Failed to load" },
];

const SORT_OPTIONS: Array<{ value: LibrarySortKey; label: string }> = [
  { value: "name", label: "Name" },
  { value: "recent", label: "Recently run" },
];

/**
 * The Library's search params, and a setter that replaces them. Replace, not
 * push, as on Runs: narrowing the list adjusts a view, and Back should leave
 * the library rather than undo it one keystroke at a time.
 */
function useLibrarySearch(): [LibrarySearch, (next: LibrarySearch) => void] {
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

export function LibraryPage() {
  const currentProject = useProjectStore((state) => state.currentProject);
  const projectId = currentProject?.id;
  const projectsLoading = useProjectStore((state) => state.isLoading);

  return (
    <div className="space-y-4">
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
    <div className="forge-ui">
      <PageHeader
        className=""
        title="Library"
        subtitle={
          <>
            The workflows your agents can run.{" "}
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
  const [search, setSearch] = useLibrarySearch();
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

  const view = useMemo(
    () =>
      libraryView({
        workflows: library.data?.workflows ?? [],
        invalid: library.data?.invalidWorkflows ?? [],
        triggers: triggers.data ?? [],
        lastRuns: lastRuns.data,
        q: search.q,
        source: search.source,
        sort: search.sort,
      }),
    [library.data, triggers.data, lastRuns.data, search.q, search.source, search.sort],
  );

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

  const tableRows: LibraryTableRow[] = view.rows.map(({ workflow, attention }) => {
    const ref = normalizeWorkflowRef(workflow.name);
    return {
      workflow: toItem(workflow),
      lastRun: lastRuns.data?.get(workflow.name),
      automationCount: view.automationCounts.get(ref) ?? 0,
      failingAutomationCount: view.failingCounts.get(ref) ?? 0,
      attention,
      onRun: workflow.status === "draft" ? undefined : () => setRunRef(workflow.name),
      actions: actionsFor(workflow),
    };
  });
  const attentionCount = view.rows.filter((row) => row.attention).length;

  const clearNarrowing = () => setSearch({ ...search, q: undefined, source: undefined });

  return (
    <div className="space-y-3" data-onboarding="workflow-hub">
      <LibraryControls search={search} onChange={setSearch} />

      {attentionCount > 0 && (
        <p className="flex items-center gap-1.5 text-xs text-warning-ink" role="status">
          <AlertTriangle className="h-3.5 w-3.5" aria-hidden="true" />
          {attentionCount === 1 ? "1 workflow needs" : `${attentionCount} workflows need`} attention: an automation that
          runs it has failed twice or more in a row. Pinned to the top.
        </p>
      )}

      {view.noMatches ? (
        <Card padding="lg" role="status">
          <p className="text-sm font-medium text-foreground">
            {search.q?.trim() ? `No workflows match “${search.q.trim()}”.` : "No workflows here."}
          </p>
          <Button className="mt-4" variant="outline" size="sm" onClick={clearNarrowing}>
            {search.q?.trim() ? "Clear search" : "Show all workflows"}
          </Button>
        </Card>
      ) : (
        <>
          {tableRows.length > 0 && (
            <div className="forge-ui">
              <DataTable<LibraryTableRow>
                ariaLabel="Workflows"
                columns={libraryColumns(search.project)}
                data={tableRows}
                layout="fixed"
                compact
                getRowKey={(row) => row.workflow.name}
                getRowProps={(row) => ({
                  "data-testid": `workflow-row-${row.workflow.name}`,
                  "data-attention": row.attention ? "true" : undefined,
                  className: row.attention ? "bg-warning/5" : undefined,
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
          {view.invalid.length > 0 && <InvalidSection workflows={view.invalid} />}
        </>
      )}

      <RunWorkflowDialog
        open={runRef !== null}
        onClose={() => setRunRef(null)}
        projectId={projectId}
        workflowRef={runRef ?? ""}
      />
    </div>
  );
}

/**
 * Search, source and sort: one compact row. Source and Sort are menus whose
 * buttons say their value, so an applied filter is always visible.
 */
function LibraryControls({ search, onChange }: { search: LibrarySearch; onChange: (next: LibrarySearch) => void }) {
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

  return (
    <div className="flex flex-wrap items-center gap-2" role="group" aria-label="Find workflows">
      <FilterSearch
        value={query}
        onChange={setQuery}
        placeholder="Search names and descriptions"
        label="Search workflows"
      />
      <SelectFilter<SourceChoice>
        label="Source"
        options={SOURCE_OPTIONS}
        value={search.source ?? "all"}
        defaultValue="all"
        onChange={(source) => onChange({ ...search, source: source === "all" ? undefined : source })}
      />
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
              <p className="text-sm font-medium text-foreground">{getWorkflowDisplayName(workflow.name, true)}</p>
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

function NoProject() {
  // The project switcher is in the header bar above, so this points at it
  // rather than sending the user out of the area to pick one.
  return (
    <div className="forge-ui">
      <EmptyState
        icon={<FolderOpen className="h-6 w-6" aria-hidden="true" />}
        title="Workflows run inside a project"
        description="Choose a project from the Project menu above to see its workflows. Runs and Automations list every project."
      />
    </div>
  );
}

function LibrarySkeleton() {
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
