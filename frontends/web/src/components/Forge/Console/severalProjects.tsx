// Copyright (c) 2026 Reliant Labs

/**
 * A console page across SEVERAL PROJECTS (U6c): when the shell shows several
 * projects (or all of them), Services and Storage list every project's rows
 * in one table with a Project column, narrowed by an in-page project filter
 * kept in the URL (`?only=<app>`), so Back, refresh and a shared link keep it.
 *
 * No page asks the reader to pick one project instead (owner, 2026-11-25):
 * every console page shows every project in scope, in one of three ways
 * (severalModeOf):
 *
 *   self      the page aggregates itself: the Overview (ProjectsOverview),
 *             Services and Storage (a Project column), Dashboards (the
 *             org-wide list narrowed to the shell's apps), and Observability's
 *             Overview and views, which hand over to the org console's page
 *             (ToOrgObservability), where the shell's nav sends them too: one
 *             read over every project in scope, narrowed by the panel's own
 *             app filter.
 *   sections  every other list page: the page itself once per project
 *             (severalProjectSections.tsx), under one title and this filter.
 *   one       a page about one object of one project (a release, a workload,
 *             a dashboard…): that object, as in one-project mode.
 */

import { useEffect, useMemo } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";

import { cn } from "@/lib/utils";

import { ProjectMark } from "../../Layout/ProjectMark";
import { sectionsAppOf, useShellSeveral } from "../../Layout/sidebar/shellProject";
import { carriedObsSearch, orgPath, withSearch, type ConsolePage, type ObsNavView } from "./paths";

export type SeveralMode = "self" | "sections" | "one";

/** How `page` shows several projects (the module comment). */
export function severalModeOf(page: ConsolePage): SeveralMode {
  switch (page.kind) {
    case "overview":
    case "services":
    case "storage":
      return "self";
    case "obs-overview":
    case "obs-view":
      return "self";
    case "workload":
    case "service":
    case "release":
    case "environment":
    case "dashboard":
    case "experiment":
    case "vulnerability":
    case "tests-run":
    case "tests-flaky-test":
      return "one";
    default:
      return "sections";
  }
}

export interface SeveralApp {
  key: string;
  /** The project's name. */
  name: string;
  /** Its forge app: the console segment every read takes. */
  app: string;
}

/** The projects the shell shows, when it shows several, each with its app; null in one-project mode. */
export function useSeveralApps(): { mode: "many" | "all"; apps: SeveralApp[] } | null {
  const { mode, rows } = useShellSeveral();
  const apps = useMemo(
    () =>
      rows.flatMap((row) => {
        const app = sectionsAppOf(row);
        return app ? [{ key: row.key, name: row.name, app }] : [];
      }),
    [rows]
  );
  return useMemo(() => (mode ? { mode, apps } : null), [mode, apps]);
}

/** The `only` filter: the apps it keeps (all of them when it names none it knows). */
export function useOnlyFilter(apps: readonly SeveralApp[]): { only: string | undefined; shown: SeveralApp[]; setOnly: (app: string | undefined) => void } {
  const search = useSearch({ strict: false }) as { only?: string };
  const navigate = useNavigate();
  const only = apps.some((candidate) => candidate.app === search.only) ? search.only : undefined;
  const shown = useMemo(() => (only ? apps.filter((candidate) => candidate.app === only) : [...apps]), [apps, only]);
  const setOnly = (app: string | undefined) =>
    void navigate({ to: ".", search: (prev: Record<string, unknown>) => ({ ...prev, only: app }), replace: true } as never);
  return { only, shown, setOnly };
}

/** The in-page project filter: All, then one chip per project. */
export function ProjectFilter({ apps, only, onChange }: { apps: readonly SeveralApp[]; only: string | undefined; onChange: (app: string | undefined) => void }) {
  const chip = (active: boolean) =>
    cn(
      "inline-flex h-8 items-center gap-1.5 rounded-full border px-3 text-ui transition-colors",
      active ? "border-primary bg-primary/10 text-ink" : "border-border text-ink-muted hover:bg-ink/5 hover:text-ink"
    );
  return (
    <div role="group" aria-label="Filter by project" className="flex flex-wrap items-center gap-2" data-testid="project-filter">
      <button type="button" className={chip(!only)} aria-pressed={!only} onClick={() => onChange(undefined)}>
        All projects
      </button>
      {apps.map((candidate) => (
        <button
          key={candidate.key}
          type="button"
          className={chip(only === candidate.app)}
          aria-pressed={only === candidate.app}
          onClick={() => onChange(only === candidate.app ? undefined : candidate.app)}
          data-testid={`project-filter-${candidate.app}`}
        >
          <ProjectMark name={candidate.name} />
          {candidate.name}
        </button>
      ))}
    </div>
  );
}

/**
 * Observability across several projects is the org console's page (the
 * shell's nav links there: Layout/sidebar/navConfig/items.tsx), which reads
 * every project in scope at once and narrows them with its app filter. An app
 * console URL of a view (a bookmark, a link, a switch to several on the page)
 * lands there too, with the view's window and filters.
 */
export function ToOrgObservability({ view }: { view?: ObsNavView }) {
  const search = useSearch({ strict: false }) as Record<string, unknown>;
  const navigate = useNavigate();
  const target = withSearch(view ? orgPath({ kind: "obs-view", view }) : orgPath({ kind: "observability" }), carriedObsSearch(search));
  useEffect(() => {
    void navigate({ href: target, replace: true });
  }, [navigate, target]);
  return null;
}
