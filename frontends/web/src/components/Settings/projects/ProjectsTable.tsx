import { useState } from "react";
import { ArrowUpRight, Pencil, Trash2 } from "lucide-react";

import Badge from "@/components/forge-ui/badge";
import DataTable, { type DataTableColumn } from "@/components/forge-ui/data_table";
import Tooltip from "@/components/forge-ui/tooltip";
import { collapseHomePath, splitPathForDisplay } from "@/lib/pathUtils";
import { formatAbsoluteTime, formatRelativeTime } from "@/lib/relativeTime";
import { cn } from "@/lib/utils";
import type { Project } from "@/store/projectStore";

import { describeProjectSource } from "./projectSource";
import { isCloudProjectPath } from "../../Projects/picker/format";
import { REMOVE_PROJECT_HINT } from "../../Projects/projectCopy";

/**
 * Every project the user has added, one row each: what it is called, where it
 * lives, where it came from, and when it was last used — with Open, Rename and
 * Remove. PURE PROPS — rows and callbacks in, no stores — so the tests hand it
 * object literals (the inline rename's draft is the only local state).
 *
 * A cloud machine's checkout path (/home/workspace/projects/…) is not shown:
 * it is the same for every cloud project, and "workspace" in it names the
 * machine, not a Reliant workspace.
 *
 * forge-ui's DataTable, the list every console page is made of, so a project
 * list reads like every other list of things in the app. The whole row opens
 * the project (getRowAction — opening selects it in the store, then
 * navigates); the action cell keeps its own buttons. Must render inside a
 * `.forge-ui` ancestor (ProjectsSection is one).
 */
export interface ProjectsTableProps {
  projects: Project[];
  currentProjectId?: string;
  onOpen: (project: Project) => void;
  onRemove: (project: Project) => void;
  /** Rename a project. Omit to hide Rename. */
  onRename?: (project: Project, name: string) => void;
  /** Disables Open on every row while a switch is in flight. */
  openingProjectId?: string | null;
}

type ProjectRow = { id: string; project: Project };

const ICON_BUTTON =
  "inline-flex h-7 w-7 items-center justify-center rounded-md text-muted-foreground transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-50";

export function ProjectsTable({
  projects,
  currentProjectId,
  onOpen,
  onRemove,
  onRename,
  openingProjectId,
}: ProjectsTableProps) {
  const [renamingId, setRenamingId] = useState<string | null>(null);
  const [draft, setDraft] = useState("");
  const commitRename = (project: Project) => {
    const name = draft.trim();
    setRenamingId(null);
    // An empty or unchanged name is a cancel.
    if (name && name !== project.name) onRename?.(project, name);
  };

  const columns: DataTableColumn<ProjectRow>[] = [
    {
      key: "name",
      header: "Project",
      className: "truncate",
      render: (_, { project }) =>
        renamingId === project.id ? (
          <input
            autoFocus
            aria-label={`Rename ${project.name}`}
            value={draft}
            onChange={(event) => setDraft(event.target.value)}
            onBlur={() => commitRename(project)}
            onKeyDown={(event) => {
              if (event.key === "Enter") commitRename(project);
              if (event.key === "Escape") setRenamingId(null);
            }}
            data-testid={`settings-project-rename-input-${project.id}`}
            className="h-7 w-full min-w-0 rounded-md border border-input bg-background px-2 text-sm text-foreground focus:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          />
        ) : (
          <span className="inline-flex max-w-full items-center gap-2 align-middle">
            <span className="truncate font-medium" title={project.name}>
              {project.name}
            </span>
            {project.id === currentProjectId && <Badge label="Current" variant="info" size="sm" />}
          </span>
        ),
    },
    {
      key: "location",
      header: "Location",
      width: "32%",
      render: (_, { project }) => <ProjectLocation path={project.path} />,
    },
    {
      key: "source",
      header: "Source",
      width: "24%",
      className: "max-md:hidden",
      headerClassName: "max-md:hidden",
      render: (_, { project }) => {
        const source = describeProjectSource(project);
        return (
          <span className="flex min-w-0 flex-col gap-0.5 py-1.5">
            <span
              className={cn("truncate", source.mono ? "font-mono text-xs" : "text-sm")}
              title={project.remote_url ?? source.primary}
            >
              {source.primary}
            </span>
            {source.secondary && <span className="truncate text-2xs text-ink-muted">{source.secondary}</span>}
          </span>
        );
      },
    },
    {
      key: "lastActive",
      header: "Last active",
      width: "7.5rem",
      className: "text-xs tabular-nums text-ink-muted max-sm:hidden",
      headerClassName: "max-sm:hidden",
      render: (_, { project }) => {
        const relative = formatRelativeTime(project.last_active);
        return relative ? (
          <time dateTime={project.last_active} title={formatAbsoluteTime(project.last_active)}>
            {relative}
          </time>
        ) : (
          <span aria-label="never">—</span>
        );
      },
    },
    {
      key: "actions",
      header: "Actions",
      srHeader: true,
      width: "7rem",
      interactive: true,
      render: (_, { project }) => (
        <span className="flex items-center justify-end gap-0.5">
          <Tooltip content="Open" delay={300}>
            <button
              type="button"
              onClick={() => onOpen(project)}
              disabled={!!openingProjectId}
              aria-label={`Open ${project.name}`}
              data-testid={`settings-project-open-${project.id}`}
              className={cn(ICON_BUTTON, "hover:bg-muted hover:text-foreground")}
            >
              <ArrowUpRight className="h-4 w-4" aria-hidden="true" />
            </button>
          </Tooltip>
          {onRename && (
            <Tooltip content="Rename" delay={300}>
              <button
                type="button"
                onClick={() => {
                  setRenamingId(project.id);
                  setDraft(project.name);
                }}
                aria-label={`Rename ${project.name}`}
                data-testid={`settings-project-rename-${project.id}`}
                className={cn(ICON_BUTTON, "hover:bg-muted hover:text-foreground")}
              >
                <Pencil className="h-4 w-4" aria-hidden="true" />
              </button>
            </Tooltip>
          )}
          <Tooltip content={REMOVE_PROJECT_HINT} side="left" delay={300}>
            <button
              type="button"
              onClick={() => onRemove(project)}
              aria-label={`Remove ${project.name} from Reliant`}
              data-testid={`settings-project-remove-${project.id}`}
              className={cn(ICON_BUTTON, "hover:bg-destructive/10 hover:text-destructive")}
            >
              <Trash2 className="h-4 w-4" aria-hidden="true" />
            </button>
          </Tooltip>
        </span>
      ),
    },
  ];

  return (
    <div data-testid="settings-projects-table">
      <DataTable<ProjectRow>
        ariaLabel="Projects"
        columns={columns}
        data={projects.map((project) => ({ id: project.id, project }))}
        layout="fixed"
        getRowKey={(row) => row.id}
        // A row being renamed holds an input, which cannot sit inside the row's button.
        getRowAction={({ project }) =>
          renamingId === project.id || openingProjectId ? undefined : () => onOpen(project)
        }
        getRowProps={({ project }) => ({
          "data-testid": `settings-project-row-${project.id}`,
          "data-current": project.id === currentProjectId ? "true" : undefined,
          "aria-current": project.id === currentProjectId ? "true" : undefined,
        })}
      />
    </div>
  );
}

/**
 * Middle-truncated: the head gives way under pressure while the leaf directory
 * stays pinned, so two checkouts of the same repo remain distinguishable.
 */
function ProjectLocation({ path }: { path: string }) {
  if (isCloudProjectPath(path)) return <span className="text-xs text-ink-muted">On a cloud machine</span>;
  const displayPath = collapseHomePath(path);
  const { head, tail } = splitPathForDisplay(displayPath);
  return (
    <span className="flex min-w-0 items-baseline font-mono text-xs text-ink-muted" title={displayPath}>
      <span className="truncate">{head}</span>
      <span className="shrink-0 text-ink">{tail}</span>
    </span>
  );
}
