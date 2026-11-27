// Copyright (c) 2025 Reliant Labs

/**
 * The console's Observability page, in the console's frame
 * (consolePageFrame.tsx: PageHeader with one meta line, then the body).
 *
 * The BODIES are the components the console already had, mounted where the
 * IA puts them (the Overview, Releases, Dashboards and Metrics have pages of
 * their own: ../Workloads, ../Releases, ../Observability/Dashboards,
 * ../Observability/Metrics). Secrets and Activity are SecretsPage.tsx and
 * ActivityPage.tsx: every page is its own chunk, so opening Errors loads
 * neither them nor the dashboards and the metrics explorer.
 */

import { lazy, Suspense, type ComponentType } from "react";

import Card from "@/components/forge-ui/card";
import SkeletonLoader from "@/components/forge-ui/skeleton_loader";
import { isChunkLoadError, staleChunkModule } from "@/lib/lazyRoute";

import { useForgeConsole } from "./consoleContext";
import { useConsoleNavExtras } from "./consoleNavExtras";
import { Header, Page } from "./consolePageFrame";
import { ObservabilityHost } from "./ObservabilityHost";
import { OBS_NAV_LABELS } from "./paths";
import { ToOrgObservability, useSeveralApps } from "./severalProjects";

/** `name` of a lazily loaded module, as a component; a chunk a deploy removed reloads the page (lazyRoute.tsx). */
function lazyPage<M extends Record<string, unknown>, K extends keyof M & string>(load: () => Promise<M>, name: K) {
  return lazy(() =>
    load()
      .then((mod) => ({ default: mod[name] as ComponentType }))
      .catch((error: unknown) => {
        if (!isChunkLoadError(error)) throw error;
        return { default: staleChunkModule(error, "default").default as ComponentType };
      })
  );
}

// Each its own chunk: an Observability view does not pay for them.
const DashboardsPage = lazyPage(() => import("../Observability/Dashboards/DashboardsPages"), "DashboardsPage");
const MetricsExplorerPage = lazyPage(() => import("../Observability/Metrics/MetricsExplorerPage"), "MetricsExplorerPage");

function PageLoading() {
  return (
    <Card padding="none" aria-label="Loading" aria-busy="true">
      <SkeletonLoader variant="table-row" count={4} />
    </Card>
  );
}

// ── Observability ───────────────────────────────────────────────────────────

/**
 * Logs, Traces, Profiles, Alerts — and Metrics, the explorer
 * (../Observability/Metrics). Dashboards has a route of its own
 * (observability/dashboards), which wins.
 */
export function ObservabilityViewPage() {
  const { page } = useForgeConsole();
  const { subtitle, intro, listPage } = useConsoleNavExtras();
  const several = useSeveralApps();
  if (page.kind !== "obs-view") return null;
  // Several projects: every view but Dashboards (which lists theirs itself) is the org console's.
  if (several && page.view !== "dashboards") return <ToOrgObservability view={page.view} />;
  if (page.view === "dashboards") {
    return (
      <Suspense fallback={<PageLoading />}>
        <DashboardsPage />
      </Suspense>
    );
  }
  if (page.view === "metrics") {
    return (
      <Page testId="console-metrics">
        <Header title={OBS_NAV_LABELS.metrics} subtitle={subtitle?.("metrics") ?? undefined} />
        <Suspense fallback={<PageLoading />}>
          <MetricsExplorerPage />
        </Suspense>
      </Page>
    );
  }
  const own = (
    <>
      <Header title={OBS_NAV_LABELS[page.view]} subtitle={subtitle?.(page.view) ?? undefined} />
      {intro?.(page.view)}
      <ObservabilityHost key={page.view} view={page.view} />
    </>
  );
  // A host's own body for the view (the led console's list pages) may stand in for it.
  return <Page testId={`console-${page.view}`}>{listPage?.(page.view, own) ?? own}</Page>;
}
