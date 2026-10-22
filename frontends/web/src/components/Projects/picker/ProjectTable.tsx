import { ChevronDown, ChevronUp, ChevronsUpDown, Cloud, GitBranch, Pencil, Trash2 } from "lucide-react";

import Badge from "@/components/forge-ui/badge";
import { Tooltip } from "@/components/ui/Tooltip";
import { cn } from "@/lib/utils";
import type { Project } from "@/store/projectStore";

import {
  displayPath,
  formatAbsolute,
  formatLastActive,
  isCloudProjectPath,
  splitPathForDisplay,
  type SortDir,
  type SortMode,
} from "./format";
import { pickerIconButton } from "./buttonStyles";

/**
 * The picker's project table, in forge's table vocabulary (see
 * Forge/Overview/EnvironmentTable.tsx): uppercase 2xs headers, rows divided by
 * `border-border`, one fact per column.
 *
 * Two modes share the one table:
 *   - browsing  the whole row opens the project; rename / remove are icon
 *               buttons that appear on row hover or keyboard focus
 *   - selecting a checkbox column appears, and a row click toggles selection
 *               instead of opening, so a mis-click during cleanup can't yank
 *               the user into a workspace
 *
 * The scroll container is the table wrapper, with a sticky header, so column
 * meanings never scroll away and a long list cannot push the page off-screen.
 */

const HEADER_CELL =
  "whitespace-nowrap px-3 py-2 text-left text-2xs font-medium uppercase tracking-wide text-muted-foreground";
const CELL = "px-3 py-2.5 align-middle";

export interface ProjectTableProps {
  projects: Project[];
  sortMode: SortMode;
  sortDir: SortDir;
  onSort: (mode: SortMode) => void;
  onOpen: (project: Project) => void;
  isSelecting: boolean;
  selectedIds: Set<string>;
  onToggleSelected: (projectId: string) => void;
  onSetSelected: (projectIds: string[], selected: boolean) => void;
  renamingId: string | null;
  renameDraft: string;
  onRenameDraftChange: (value: string) => void;
  onBeginRename: (project: Project) => void;
  onCommitRename: (project: Project) => void;
  onCancelRename: () => void;
  onRemove: (project: Project) => void;
}

export function ProjectTable({
  projects,
  sortMode,
  sortDir,
  onSort,
  onOpen,
  isSelecting,
  selectedIds,
  onToggleSelected,
  onSetSelected,
  renamingId,
  renameDraft,
  onRenameDraftChange,
  onBeginRename,
  onCommitRename,
  onCancelRename,
  onRemove,
}: ProjectTableProps) {
  const allVisibleSelected = projects.length > 0 && projects.every((p) => selectedIds.has(p.id));

  return (
    <div
      className="min-h-0 overflow-y-auto overscroll-contain rounded-lg border border-border bg-card"
      data-testid="project-table"
    >
      <table className="w-full table-fixed border-collapse text-sm">
        <caption className="sr-only">
          Your projects. Select a row to open it. Column headers sort the list.
        </caption>
        <colgroup>
          {isSelecting && <col className="w-10" />}
          <col className="w-[34%]" />
          <col />
          <col className="w-28" />
          <col className="w-20" />
        </colgroup>
        {/* bg-card on the sticky head so rows scrolling under it don't show
            through. */}
        <thead className="sticky top-0 z-10 bg-card">
          <tr className="border-b border-border">
            {isSelecting && (
              <th scope="col" className={HEADER_CELL}>
                <input
                  type="checkbox"
                  className="h-4 w-4 align-middle accent-primary"
                  aria-label="Select all projects shown"
                  checked={allVisibleSelected}
                  onChange={(e) =>
                    // Select-all applies to what's currently visible, so it
                    // composes with search instead of silently selecting
                    // filtered-out projects.
                    onSetSelected(
                      projects.map((p) => p.id),
                      e.target.checked,
                    )
                  }
                  data-testid="project-select-all"
                />
              </th>
            )}
            <SortableHeader mode="name" label="Name" activeMode={sortMode} dir={sortDir} onSort={onSort} />
            <SortableHeader mode="path" label="Location" activeMode={sortMode} dir={sortDir} onSort={onSort} />
            <SortableHeader mode="recent" label="Last used" activeMode={sortMode} dir={sortDir} onSort={onSort} />
            <th scope="col" className={HEADER_CELL}>
              <span className="sr-only">Actions</span>
            </th>
          </tr>
        </thead>
        <tbody>
          {projects.map((project) => (
            <ProjectRow
              key={project.id}
              project={project}
              isSelecting={isSelecting}
              isSelected={selectedIds.has(project.id)}
              isRenaming={renamingId === project.id}
              renameDraft={renameDraft}
              onOpen={onOpen}
              onToggleSelected={onToggleSelected}
              onRenameDraftChange={onRenameDraftChange}
              onBeginRename={onBeginRename}
              onCommitRename={onCommitRename}
              onCancelRename={onCancelRename}
              onRemove={onRemove}
            />
          ))}
        </tbody>
      </table>
    </div>
  );
}

function ProjectRow({
  project,
  isSelecting,
  isSelected,
  isRenaming,
  renameDraft,
  onOpen,
  onToggleSelected,
  onRenameDraftChange,
  onBeginRename,
  onCommitRename,
  onCancelRename,
  onRemove,
}: {
  project: Project;
  isSelecting: boolean;
  isSelected: boolean;
  isRenaming: boolean;
  renameDraft: string;
  onOpen: (project: Project) => void;
  onToggleSelected: (projectId: string) => void;
  onRenameDraftChange: (value: string) => void;
  onBeginRename: (project: Project) => void;
  onCommitRename: (project: Project) => void;
  onCancelRename: () => void;
  onRemove: (project: Project) => void;
}) {
  const { head, tail } = splitPathForDisplay(project.path);
  const isCloud = isCloudProjectPath(project.path);

  const activate = () => {
    if (isRenaming) return;
    if (isSelecting) onToggleSelected(project.id);
    else onOpen(project);
  };

  return (
    <tr
      className={cn(
        "group border-b border-border last:border-0 transition-colors",
        // bg-muted is interaction state here (hover / selected), never
        // structure — the row sits on bg-card either way.
        isSelected ? "bg-primary/10" : "hover:bg-muted/60 focus-within:bg-muted/60",
        !isRenaming && "cursor-pointer",
      )}
      onClick={activate}
      data-testid="project-item"
    >
      {isSelecting && (
        <td className={CELL}>
          <input
            type="checkbox"
            checked={isSelected}
            onChange={() => onToggleSelected(project.id)}
            onClick={(e) => e.stopPropagation()}
            aria-label={`Select ${project.name}`}
            className="h-4 w-4 align-middle accent-primary"
            data-testid="project-select"
          />
        </td>
      )}

      <td className={CELL}>
        {isRenaming ? (
          <input
            autoFocus
            value={renameDraft}
            onChange={(e) => onRenameDraftChange(e.target.value)}
            onClick={(e) => e.stopPropagation()}
            onBlur={() => onCommitRename(project)}
            onKeyDown={(e) => {
              if (e.key === "Enter") onCommitRename(project);
              if (e.key === "Escape") onCancelRename();
            }}
            aria-label={`Rename ${project.name}`}
            className="h-7 w-full rounded-md border border-border-strong bg-background px-2 text-sm text-ink focus:outline-none focus:ring-2 focus:ring-accent/40"
            data-testid="project-rename-input"
          />
        ) : (
          // The row's keyboard affordance. A <tr> cannot take focus
          // meaningfully, so while browsing the name is a real button: the
          // row's onClick handles the mouse, and Enter/Space on the button
          // fires a click that bubbles to it. While selecting, the checkbox is
          // the control, so the name is plain text.
          <div className="flex min-w-0 items-center gap-2">
            {isSelecting ? (
              <span className="min-w-0 truncate font-medium text-ink" title={project.name}>
                {project.name}
              </span>
            ) : (
              <button
                type="button"
                className="min-w-0 truncate rounded-sm text-left font-medium text-ink focus:outline-none focus-visible:ring-2 focus-visible:ring-accent/60"
                title={project.name}
                aria-label={`Open ${project.name}`}
                data-testid="project-open"
              >
                {project.name}
              </button>
            )}
            {project.is_git_repo && (
              <GitBranch
                className="h-3 w-3 shrink-0 text-ink-subtle"
                aria-label="Git repository"
                role="img"
              />
            )}
            {isCloud && <Badge label="Cloud" variant="neutral" size="sm" />}
          </div>
        )}
      </td>

      {/* Path, middle-truncated: the head collapses under pressure while the
          leaf directory is pinned, so two checkouts of the same repo stay
          distinguishable. The full path is the cell's title. */}
      <td className={CELL} title={displayPath(project.path)}>
        <div className="flex min-w-0 items-baseline font-mono text-xs text-ink-muted">
          {isCloud && <Cloud className="mr-1.5 h-3 w-3 shrink-0 self-center" aria-hidden="true" />}
          <span className="truncate">{head}</span>
          <span className="shrink-0">{tail}</span>
        </div>
      </td>

      <td
        className={cn(CELL, "whitespace-nowrap text-xs tabular-nums text-ink-muted")}
        title={formatAbsolute(project.last_active)}
      >
        {formatLastActive(project.last_active)}
      </td>

      <td className={cn(CELL, "text-right")}>
        {!isRenaming && (
          // Hidden until the row is hovered or something in it has focus, so
          // a dense list reads as a list. Always visible while selecting, and
          // on touch screens where there is no hover.
          <div
            className={cn(
              "flex items-center justify-end gap-0.5 transition-opacity",
              !isSelecting &&
                "pointer-fine:opacity-0 pointer-fine:group-hover:opacity-100 pointer-fine:group-focus-within:opacity-100",
            )}
          >
            <Tooltip content="Rename" placement="top" delay={300}>
              <button
                type="button"
                onClick={(e) => {
                  e.stopPropagation();
                  onBeginRename(project);
                }}
                aria-label={`Rename ${project.name}`}
                className={pickerIconButton()}
                data-testid="project-rename"
              >
                <Pencil className="h-3.5 w-3.5" aria-hidden="true" />
              </button>
            </Tooltip>
            <Tooltip content="Remove from Reliant (files stay on disk)" placement="top" delay={300}>
              <button
                type="button"
                onClick={(e) => {
                  e.stopPropagation();
                  onRemove(project);
                }}
                aria-label={`Remove ${project.name}`}
                className={pickerIconButton("danger")}
                data-testid="project-remove"
              >
                <Trash2 className="h-3.5 w-3.5" aria-hidden="true" />
              </button>
            </Tooltip>
          </div>
        )}
      </td>
    </tr>
  );
}

// A sortable column header. Clicking the active column flips direction;
// clicking another switches to it. aria-sort keeps that legible to screen
// readers.
function SortableHeader({
  mode,
  label,
  activeMode,
  dir,
  onSort,
}: {
  mode: SortMode;
  label: string;
  activeMode: SortMode;
  dir: SortDir;
  onSort: (mode: SortMode) => void;
}) {
  const isActive = mode === activeMode;
  return (
    <th
      scope="col"
      aria-sort={isActive ? (dir === "asc" ? "ascending" : "descending") : "none"}
      className={HEADER_CELL}
    >
      <button
        type="button"
        onClick={() => onSort(mode)}
        className={cn(
          "group/sort inline-flex items-center gap-1 rounded-sm uppercase tracking-wide transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-accent/60",
          isActive ? "text-ink" : "hover:text-ink",
        )}
        data-testid={`project-sort-${mode}`}
      >
        {label}
        {isActive ? (
          dir === "asc" ? (
            <ChevronUp className="h-3 w-3" aria-hidden="true" />
          ) : (
            <ChevronDown className="h-3 w-3" aria-hidden="true" />
          )
        ) : (
          // Reserve the caret's space on inactive headers so labels don't
          // shift horizontally when the sort column changes.
          <ChevronsUpDown
            className="h-3 w-3 opacity-0 transition-opacity group-hover/sort:opacity-60"
            aria-hidden="true"
          />
        )}
      </button>
    </th>
  );
}
