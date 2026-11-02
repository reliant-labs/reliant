// Copyright (c) 2025 Reliant Labs

/**
 * Observability › Dashboards, org-wide: the dashboards of every app in one
 * list. Reading is here; everything that writes (open, edit, copy, save, new)
 * belongs to an app, so each row links to its dashboard inside that app's
 * console (and a dashboard with no app, saved before projects existed, has no
 * console to open in and is not a link).
 *
 * The App filter rides the shared `apps` URL key, so narrowing here carries to
 * Logs, Traces and the rest. The list's own filters (text, source, owner) are
 * this page's, not the URL's: `q` is also the Logs query, and a search typed
 * here must not turn up there.
 */

import { useCallback, useMemo, useState } from "react";
import { useNavigate, useSearch } from "@tanstack/react-router";

import type { DashboardRef } from "@/services/controlPlane/observability/dashboardTypes";

import { consolePath } from "../Console/paths";
import { DashboardsList, type DashboardsAppFilter, type DashboardsSearch } from "../Observability/Dashboards/DashboardsList";
import type { ObsEnvironment } from "../Observability/obsPlace";

/** Where a dashboard opens: its app's console. Empty: it has no app, so there is nowhere to open it. */
export function orgDashboardHref(ref: DashboardRef): string {
  if (!ref.projectId || ref.kind === "template") return "";
  return consolePath(ref.projectId, { kind: "dashboard", dashKind: ref.kind, key: ref.key });
}

const HREFS = { dashboard: orgDashboardHref };

export function OrgDashboards({ environments }: { environments: readonly ObsEnvironment[] }) {
  const navigate = useNavigate();
  const search = useSearch({ strict: false }) as { apps?: string[] };
  const [filters, setFilters] = useState<DashboardsSearch>({});

  const onSearch = useCallback((next: Partial<DashboardsSearch>) => setFilters((prev) => ({ ...prev, ...next })), []);
  const navigateTo = useCallback((href: string) => void navigate({ href }), [navigate]);

  const selected = search.apps;
  const appFilter = useMemo<DashboardsAppFilter>(
    () => ({
      selected: selected ?? [],
      onChange: (apps) =>
        void navigate({
          to: ".",
          search: (prev: Record<string, unknown>) => ({ ...prev, apps: apps.length ? apps : undefined }),
          replace: true,
        }),
    }),
    [selected, navigate]
  );

  return (
    <div data-testid="org-dashboards">
      <DashboardsList
        env="all"
        environments={environments}
        search={filters}
        onSearch={onSearch}
        hrefs={HREFS}
        navigateTo={navigateTo}
        appFilter={appFilter}
      />
    </div>
  );
}
