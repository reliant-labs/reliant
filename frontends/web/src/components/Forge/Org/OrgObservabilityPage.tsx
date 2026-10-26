// Copyright (c) 2025 Reliant Labs

/**
 * One Observability page of the ORG console — /forge/observability[/:view] —
 * hosting the same views the app console does (ObservabilityPanel), over every
 * app the organization has deployed.
 *
 * The org counterpart of Console/ObservabilityHost, and the difference is the
 * scope: app and environment are FILTERS here (the `apps` and `envs` URL keys,
 * `app/env` names) which the panel draws and reads itself for an org place,
 * rather than a top-bar scope this page translates.
 *
 *   view   is the PAGE. A view switch from inside a view (a log line's "open
 *          trace") navigates to the org page hosting that view, carrying the
 *          window, filters and the app/env selection (orgObsPages.ts).
 *   Metrics and Dashboards are not the panel's: Metrics has no view yet (the
 *          page says where metric panels are), Dashboards is the org-wide list
 *          (OrgDashboards).
 */

import { lazy, Suspense, useCallback, useMemo } from "react";
import { useLocation, useNavigate, useSearch } from "@tanstack/react-router";
import { Activity } from "lucide-react";

import Card from "@/components/forge-ui/card";
import EmptyState from "@/components/forge-ui/empty_state";
import Link from "@/components/forge-ui/link";
import PageHeader from "@/components/forge-ui/page_header";
import SkeletonLoader from "@/components/forge-ui/skeleton_loader";
import { useOrgEnvironments } from "@/hooks/apps-queries";
import { isChunkLoadError, staleChunkModule } from "@/lib/lazyRoute";

import { carriedOrgSearch, OBS_NAV_LABELS, orgPageOf, orgPath, withSearch, type OrgPage } from "../Console/paths";
import { orgPlaceOf } from "../Observability/obsPlace";
import type { ObsNavigate, ObsSearch } from "../Observability/obsSearch";
import { CloudNotice } from "../SourceNotices";
import { OrgDashboards } from "./OrgDashboards";
import { obsViewOfOrgPage, orgPageOfObsView, sameOrgPage } from "./orgObsPages";

// Its own chunk: uPlot and the views load only on a page that shows them.
const ObservabilityPanel = lazy(() =>
  import("../Observability/ObservabilityPanel").catch((error: unknown) => {
    if (!isChunkLoadError(error)) throw error;
    return staleChunkModule(error, "default") as unknown as typeof import("../Observability/ObservabilityPanel");
  })
);

const OVERVIEW: OrgPage = { kind: "observability" };

export function OrgObservabilityPage() {
  const { pathname } = useLocation();
  const navigate = useNavigate();
  const search = useSearch({ strict: false }) as Record<string, unknown>;
  const page = useMemo(() => orgPageOf(pathname) ?? OVERVIEW, [pathname]);
  const view = obsViewOfOrgPage(page);

  // The same org-wide read the Apps page lists apps from (hooks/apps-queries.ts).
  const orgEnvs = useOrgEnvironments();
  const data = orgEnvs.data;
  const { environments, apps } = useMemo(() => orgPlaceOf(data?.envs ?? []), [data]);
  const place = useMemo(() => ({ kind: "org" as const, environments, apps }), [environments, apps]);

  const obsSearch = useMemo<ObsSearch>(
    () => ({ ...(carriedOrgSearch(search) as ObsSearch), view: view ?? undefined }),
    [search, view]
  );

  const navigateObs = useCallback<ObsNavigate>(
    (next, options) => {
      const { view: nextView, ...rest } = next;
      const target = "view" in next ? orgPageOfObsView(nextView) : page;
      if (!sameOrgPage(target, page)) {
        void navigate({
          href: withSearch(orgPath(target), { ...carriedOrgSearch(search), ...rest }),
          replace: options?.replace,
        });
        return;
      }
      void navigate({
        to: ".",
        search: (prev: Record<string, unknown>) => ({ ...prev, ...rest }),
        replace: options?.replace,
      });
    },
    [navigate, page, search]
  );

  const title = page.kind === "obs-view" ? OBS_NAV_LABELS[page.view] : "Observability";
  const appCount = apps.length;
  const meta = data && environments.length > 0 && (
    <>
      <span>
        {appCount} app{appCount === 1 ? "" : "s"} · {environments.length} environment{environments.length === 1 ? "" : "s"}
      </span>
      <span>Narrow by app and environment below</span>
    </>
  );

  const loading = (
    <Card padding="none" aria-label="Loading" aria-busy="true">
      <SkeletonLoader variant="table-row" count={4} />
    </Card>
  );

  let body;
  if (orgEnvs.isLoading) {
    body = <div data-testid="org-obs-loading">{loading}</div>;
  } else if (environments.length === 0) {
    body = (
      <div data-testid="org-obs-no-envs">
        <EmptyState
          icon={<Activity className="h-6 w-6" aria-hidden="true" />}
          title="No environments to observe yet"
          description="Telemetry is filed under the environments Reliant records. Deploy one through Reliant (forge env deploy <env>) and its logs, traces, metrics and dashboards appear here, across every app."
        />
      </div>
    );
  } else if (page.kind === "obs-view" && page.view === "dashboards") {
    body = <OrgDashboards environments={environments} />;
  } else if (page.kind === "obs-view" && page.view === "metrics") {
    body = (
      <Card data-testid="org-metrics">
        <p className="text-sm text-ink-muted">
          A metrics explorer — any metric, filtered and grouped — lands here. Until then, metric panels live in{" "}
          <Link
            href={withSearch(orgPath({ kind: "obs-view", view: "dashboards" }), carriedOrgSearch(search))}
            className="text-ink underline-offset-2 hover:underline"
          >
            Dashboards
          </Link>
          .
        </p>
      </Card>
    );
  } else {
    body = (
      <Suspense fallback={loading}>
        <ObservabilityPanel place={place} search={obsSearch} navigate={navigateObs} chrome="console" />
      </Suspense>
    );
  }

  return (
    <div className="space-y-6" data-testid="org-observability" data-page={page.kind === "obs-view" ? page.view : "overview"}>
      <PageHeader title={title} meta={meta || undefined} className="mb-0" />
      <CloudNotice availability={data?.availability} detail={data?.detail} />
      {body}
    </div>
  );
}
