// Copyright (c) 2026 Reliant Labs

/**
 * A console page across SEVERAL PROJECTS (U6c): when the shell shows several
 * projects (or all of them), a list page (Dashboards) reads every project's
 * rows. Pages that are one project's by nature (Releases, Environments, Secrets,
 * Domains, the telemetry explorers) keep the in-page picker in the frame's
 * header (Layout/sidebar/AppPageProjectPicker.tsx) instead.
 */

import { useMemo } from "react";

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
