// Copyright (c) 2026 Reliant Labs

/**
 * The frame every aggregate page shares (registry.ts), top to bottom:
 *
 *   Title                          one heading for the page
 *   N projects, <coverage>.        the scope, and what the figures cover
 *   [totals strip]                 optional: figures the BACKEND computed over every project in scope
 *   [drill-down hint]              optional: what selecting one project shows that this page does not
 *   Project | … | ›                one row per project, opening that project's own page
 *
 * Totals are never summed in the browser: a page passes the backend's
 * aggregate, or none. A project's row reads that project's slice; a row the
 * reader may not see, or with nothing recorded, says so (ProjectRowState).
 */

import type { ComponentType, ReactNode } from "react";
import { ChevronRight } from "lucide-react";

import StatGrid from "@/components/forge-ui/stat_grid";
import { cn } from "@/lib/utils";

import { ProjectMark } from "../../../Layout/ProjectMark";
import type { ConsolePage } from "../paths";
import type { SeveralApp } from "../severalProjects";
import { drillDownHref, isPlainClick, useDrillDown } from "./drillDown";

/** "N projects" or "Every project": the scope an aggregate covers. */
export function scopeLabel(mode: "many" | "all", count: number): string {
  if (mode === "all") return "Every project";
  return count === 1 ? "1 project" : `${count} projects`;
}

/** One figure of the totals strip, as the backend computed it over every project in scope. */
export interface AggregateTotal {
  label: string;
  /** Already formatted ("1,204", "92.4%"). */
  value: string;
  trend?: { direction: "up" | "down" | "flat"; value: string };
}

export interface AggregatePageFrameProps {
  title: string;
  mode: "many" | "all";
  /** How many projects are in scope. */
  count: number;
  /** What the figures cover, after the scope: "in prod over the last 24 hours". */
  coverage?: string;
  /** The backend's totals over every project in scope; undefined shows no strip. */
  totals?: readonly AggregateTotal[];
  /** A DrillDownHint, where this page differs from one project's. */
  hint?: ReactNode;
  /** The per-project breakdown (ProjectBreakdown). */
  children: ReactNode;
  testId?: string;
}

/** An aggregate page: title, scope, optional totals and hint, then the per-project breakdown. */
export function AggregatePage({ title, mode, count, coverage, totals, hint, children, testId = "aggregate-page" }: AggregatePageFrameProps) {
  const scope = scopeLabel(mode, count);
  return (
    <div className="mx-auto min-w-0 max-w-[var(--page-max)] space-y-5" data-testid={testId}>
      <div>
        <h1 className="text-title text-ink">{title}</h1>
        <p className="mt-1 text-ui text-ink-muted" data-testid="aggregate-scope">
          {coverage ? `${scope}, ${coverage}.` : `${scope}.`}
        </p>
      </div>
      {totals && totals.length > 0 && <AggregateTotals totals={totals} />}
      {hint}
      {children}
    </div>
  );
}

/** The totals strip: the backend's figures over every project in scope. */
export function AggregateTotals({ totals }: { totals: readonly AggregateTotal[] }) {
  const columns = Math.min(4, Math.max(2, totals.length)) as 2 | 3 | 4;
  return (
    <div data-testid="aggregate-totals">
      <StatGrid stats={totals.map((total) => ({ ...total }))} columns={columns} />
    </div>
  );
}

/**
 * Whether a project's row has data to show: `ok`; `no-access`, the reader
 * may not see this project's data (the row opens nothing); `no-data`, it has
 * nothing recorded yet (the row still opens its page, which says how to start).
 */
export type ProjectAccess = "ok" | "no-access" | "no-data";

const ACCESS_WORDS: Record<Exclude<ProjectAccess, "ok">, string> = {
  "no-access": "No access",
  "no-data": "No data yet",
};

/** A row's access state, across its data columns. */
export function ProjectRowState({ state }: { state: Exclude<ProjectAccess, "ok"> }) {
  return (
    // Runtime geometry: every data column, between the Project column and the chevron.
    <span className="min-w-0 truncate text-ink-muted" style={{ gridColumn: "2 / -2" }} data-testid={`project-row-state-${state}`}>
      {ACCESS_WORDS[state]}
    </span>
  );
}

export interface BreakdownColumn {
  /** The column's head. */
  header: ReactNode;
  /** Its grid track; `minmax(0,1fr)` by default. */
  track?: string;
  /** Where the head sits; a row's cell aligns itself the same way. */
  align?: "start" | "end";
}

export interface ProjectBreakdownProps {
  /** The projects, one row each. */
  projects: readonly SeveralApp[];
  /** The page a row opens, for its project alone. */
  page: ConsolePage;
  /** The data columns after Project. */
  columns: readonly BreakdownColumn[];
  /** A row's data cells, one element per column: a component, so a row may read its own project's hooks. */
  Cells: ComponentType<{ project: SeveralApp }>;
  /** A row's access; `ok` (cells) by default. */
  accessOf?: (project: SeveralApp) => ProjectAccess;
  /** The test id of a project's row; `aggregate-row-<key>` by default. */
  rowTestId?: (project: SeveralApp) => string;
}

const PROJECT_TRACK = "minmax(0,2fr)";
const CHEVRON_TRACK = "2rem";

/** The per-project breakdown: Project, the page's columns, a way into each project alone. */
export function ProjectBreakdown({ projects, page, columns, Cells, accessOf, rowTestId }: ProjectBreakdownProps) {
  const template = [PROJECT_TRACK, ...columns.map((column) => column.track ?? "minmax(0,1fr)"), CHEVRON_TRACK].join(" ");
  return (
    <div className="overflow-hidden rounded-xl border border-border" data-testid="project-breakdown">
      <div className="grid items-center gap-4 border-b border-border px-4 py-2 text-meta text-ink-muted" style={{ gridTemplateColumns: template }}>
        <span>Project</span>
        {columns.map((column, index) => (
          <span key={index} className={column.align === "end" ? "text-right" : undefined}>
            {column.header}
          </span>
        ))}
        <span />
      </div>
      {projects.map((project) => (
        <BreakdownRow
          key={project.key}
          project={project}
          page={page}
          template={template}
          access={accessOf?.(project) ?? "ok"}
          Cells={Cells}
          testId={rowTestId ? rowTestId(project) : `aggregate-row-${project.key}`}
        />
      ))}
    </div>
  );
}

const ROW =
  "grid items-center gap-4 border-b border-border-subtle px-4 py-3 text-ui text-ink last:border-b-0";

function BreakdownRow({
  project,
  page,
  template,
  access,
  Cells,
  testId,
}: {
  project: SeveralApp;
  page: ConsolePage;
  template: string;
  access: ProjectAccess;
  Cells: ComponentType<{ project: SeveralApp }>;
  testId: string;
}) {
  const drillDown = useDrillDown();
  const name = (
    <span className="flex min-w-0 items-center gap-2">
      <ProjectMark name={project.name} />
      <span className="min-w-0 truncate font-medium">{project.name}</span>
      {project.app !== project.name && <span className="min-w-0 truncate text-meta text-ink-muted">{project.app}</span>}
    </span>
  );
  const cells = access === "ok" ? <Cells project={project} /> : <ProjectRowState state={access} />;
  // A project the reader may not see opens nothing: its page would only say so again.
  if (access === "no-access") {
    return (
      <div className={ROW} style={{ gridTemplateColumns: template }} data-testid={testId} data-access={access}>
        {name}
        {cells}
        <span />
      </div>
    );
  }
  const href = drillDownHref(project, page);
  return (
    <a
      href={href}
      onClick={(event) => {
        if (!isPlainClick(event)) return;
        event.preventDefault();
        // Into this project alone: the shell's one project, on the same page of its app.
        drillDown(project, page);
      }}
      data-testid={testId}
      data-access={access}
      style={{ gridTemplateColumns: template }}
      className={cn("no-color", ROW, "transition-colors hover:bg-ink/5 focus:outline-none focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring")}
    >
      {name}
      {cells}
      <ChevronRight className="size-4 justify-self-end text-ink-muted" aria-hidden="true" />
    </a>
  );
}
