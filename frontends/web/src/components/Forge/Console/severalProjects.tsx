// Copyright (c) 2026 Reliant Labs

/**
 * A console page across SEVERAL PROJECTS (U6c): when the shell shows several
 * projects (or all of them), Services and Storage list every project's rows
 * in one table with a Project column, narrowed by an in-page project filter
 * kept in the URL (`?only=<app>`), so Back, refresh and a shared link keep it.
 * Pages that are one project's by nature (Releases, Environments, Secrets,
 * Domains, the telemetry explorers) keep the in-page picker in the frame's
 * header (Layout/sidebar/AppPageProjectPicker.tsx) instead.
 */

import { useMemo } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";

import { cn } from "@/lib/utils";

import { ProjectMark } from "../../Layout/ProjectMark";
import { sectionsAppOf, useShellSeveral } from "../../Layout/sidebar/shellProject";

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
  return mode ? { mode, apps } : null;
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
