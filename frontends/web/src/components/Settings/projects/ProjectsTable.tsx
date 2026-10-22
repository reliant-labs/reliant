import { ArrowUpRight, Trash2 } from "lucide-react";

import Badge from "@/components/forge-ui/badge";
import { Tooltip } from "@/components/ui/Tooltip";
import { collapseHomePath, splitPathForDisplay } from "@/lib/pathUtils";
import { formatAbsoluteTime, formatRelativeTime } from "@/lib/relativeTime";
import { cn } from "@/lib/utils";
import type { Project } from "@/store/projectStore";

import { describeProjectSource } from "./projectSource";

/**
 * Every project the user has added, one row each: what it is called, where it
 * lives, where it came from, and when it was last used. PURE PROPS — rows and
 * callbacks in, no stores — so the tests hand it object literals.
 *
 * A real table (`<th scope>` per column, actions right-aligned), in the same
 * vocabulary as forge's EnvironmentTable, so a project list reads like every
 * other list of things in the app.
 */
export interface ProjectsTableProps {
  projects: Project[];
  currentProjectId?: string;
  onOpen: (project: Project) => void;
  onRemove: (project: Project) => void;
  /** Disables Open on every row while a switch is in flight. */
  openingProjectId?: string | null;
}

const HEADER_CELL =
  "whitespace-nowrap px-3 py-2 text-left text-2xs font-medium uppercase tracking-wide text-muted-foreground";
const CELL = "px-3 py-2.5 align-middle";
const ICON_BUTTON =
  "inline-flex h-7 w-7 items-center justify-center rounded-md text-muted-foreground transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-50";

export function ProjectsTable({
  projects,
  currentProjectId,
  onOpen,
  onRemove,
  openingProjectId,
}: ProjectsTableProps) {
  return (
    <div className="overflow-x-auto rounded-lg border border-border bg-card" data-testid="settings-projects-table">
      <table className="w-full min-w-[40rem] table-fixed border-collapse text-sm">
        <caption className="sr-only">
          Every project in Reliant: its name, where it lives, where it came from, and when you last used it.
        </caption>
        <colgroup>
          <col className="w-[24%]" />
          <col />
          <col className="w-[24%]" />
          <col className="w-28" />
          <col className="w-24" />
        </colgroup>
        <thead>
          <tr className="border-b border-border">
            <th scope="col" className={HEADER_CELL}>
              Project
            </th>
            <th scope="col" className={HEADER_CELL}>
              Location
            </th>
            <th scope="col" className={HEADER_CELL}>
              Source
            </th>
            <th scope="col" className={HEADER_CELL}>
              Last active
            </th>
            <th scope="col" className={cn(HEADER_CELL, "text-right")}>
              <span className="sr-only">Actions</span>
            </th>
          </tr>
        </thead>
        <tbody>
          {projects.map((project) => {
            const isCurrent = project.id === currentProjectId;
            const displayPath = collapseHomePath(project.path);
            const { head, tail } = splitPathForDisplay(displayPath);
            const source = describeProjectSource(project);
            const relative = formatRelativeTime(project.last_active);
            return (
              <tr
                key={project.id}
                data-testid={`settings-project-row-${project.id}`}
                data-current={isCurrent || undefined}
                aria-current={isCurrent ? "true" : undefined}
                className="border-b border-border/60 last:border-0"
              >
                <th scope="row" className={cn(CELL, "text-left font-normal")}>
                  <div className="flex min-w-0 items-center gap-2">
                    <span className="truncate font-medium text-foreground" title={project.name}>
                      {project.name}
                    </span>
                    {isCurrent && <Badge label="Current" variant="info" size="sm" />}
                  </div>
                </th>

                {/* Middle-truncated: the head gives way under pressure while the
                    leaf directory stays pinned, so two checkouts of the same
                    repo remain distinguishable. */}
                <td className={CELL} title={displayPath}>
                  <div className="flex min-w-0 items-baseline font-mono text-xs text-muted-foreground">
                    <span className="truncate">{head}</span>
                    <span className="shrink-0 text-foreground/80">{tail}</span>
                  </div>
                </td>

                <td className={CELL}>
                  <div className="flex min-w-0 flex-col gap-0.5">
                    <span
                      className={cn("truncate text-foreground", source.mono ? "font-mono text-xs" : "text-sm")}
                      title={project.remote_url ?? source.primary}
                    >
                      {source.primary}
                    </span>
                    {source.secondary && (
                      <span className="truncate text-2xs text-muted-foreground">{source.secondary}</span>
                    )}
                  </div>
                </td>

                <td className={cn(CELL, "whitespace-nowrap text-xs tabular-nums text-muted-foreground")}>
                  {relative ? (
                    <time dateTime={project.last_active} title={formatAbsoluteTime(project.last_active)}>
                      {relative}
                    </time>
                  ) : (
                    <span aria-label="never">—</span>
                  )}
                </td>

                <td className={cn(CELL, "whitespace-nowrap")}>
                  <div className="flex items-center justify-end gap-0.5">
                    <Tooltip content={isCurrent ? "Go to project" : "Switch to this project"} delay={300}>
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
                    <Tooltip content="Remove from Reliant (files stay on disk)" placement="left" delay={300}>
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
                  </div>
                </td>
              </tr>
            );
          })}
        </tbody>
      </table>
    </div>
  );
}
