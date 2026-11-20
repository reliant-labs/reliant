// Copyright (c) 2026 Reliant Labs

/**
 * ANALYTICS (U6c, owner round 5: "I want my analytics tabs back"):
 * /forge/p/:app/analytics[/:tab].
 *
 *   overview · users · product · acquisition
 *            forge's four built-in Product analytics dashboards, one tab
 *            each (the dashboard itself: DashboardBody). The dashboard is the
 *            project's own copy when its release ships one (the index's hub
 *            tab), else the control plane's built-in of that id.
 *   events   Events & metrics: the custom events and metrics the app sends.
 *   + a registered page joining Product analytics (Experiments:
 *     consoleNavRegistry.ts `group: "product-analytics"`), as a last tab
 *     that opens its own page, only while it is registered.
 *
 * The tab is a path segment, so Back, refresh and a shared link keep it.
 */

import { useMemo } from "react";
import { FlaskConical } from "lucide-react";

import Link from "@/components/forge-ui/link";
import { useDashboardIndex } from "@/hooks/dashboard-queries";
import { useObservabilityOrg } from "@/hooks/observability-queries";
import { cn } from "@/lib/utils";
import type { DashboardRef } from "@/services/controlPlane/observability/dashboardTypes";

import { useForgeConsole } from "../Console/consoleContext";
import { consoleNavEntries } from "../Console/consoleNavRegistry";
import { Header, Page } from "../Console/consolePageFrame";
import { ENV_ALL } from "../Console/envScope";
import { PAGE_SUBTITLES } from "../Console/groupedNav";
import { ANALYTICS_TAB_LABELS, ANALYTICS_TABS, type AnalyticsTab } from "../Console/paths";
import { PRODUCT_OVERVIEW_KEY } from "../Observability/Dashboards/dashboardGroups";
import { hubsOf } from "../Observability/Dashboards/dashboardHubs";
import { builtinPageRef } from "../Observability/Dashboards/dashboardModel";
import { DashboardBody } from "../Observability/Dashboards/DashboardsPages";
import { EventsMetricsPage } from "./EventsMetricsPage";

/** The built-in dashboard id behind each dashboard tab. */
export const ANALYTICS_DASHBOARD_KEY: Record<Exclude<AnalyticsTab, "events">, string> = {
  overview: PRODUCT_OVERVIEW_KEY,
  users: "users",
  product: "product",
  acquisition: "acquisition",
};

export function AnalyticsPage() {
  const { forgeProject, env, roster, href, page } = useForgeConsole();
  const tab: AnalyticsTab = page.kind === "analytics" ? (page.tab ?? "overview") : "overview";
  const org = useObservabilityOrg();
  const envId = env === ENV_ALL ? undefined : roster.envs.find((candidate) => candidate.name === env)?.live?.id;
  const index = useDashboardIndex(org.orgId ?? null, forgeProject, envId);
  const extras = consoleNavEntries().filter((entry) => entry.group === "product-analytics");

  // The project's own copy of the tab's dashboard when the index has one, else the built-in.
  const dashRef = useMemo<DashboardRef | null>(() => {
    if (tab === "events") return null;
    const key = ANALYTICS_DASHBOARD_KEY[tab];
    const product = hubsOf(index.data?.dashboards ?? [], forgeProject).find((h) => h.id === "product");
    return product?.tabs.find((candidate) => candidate.ref.key === key)?.ref ?? builtinPageRef(key, forgeProject);
  }, [tab, index.data, forgeProject]);

  return (
    <Page testId="console-analytics">
      <Header title="Analytics" subtitle={PAGE_SUBTITLES.analytics} />
      <nav aria-label="Analytics" className="-mt-1 flex gap-1 overflow-x-auto border-b border-border" data-testid="analytics-tabs">
        {ANALYTICS_TABS.map((candidate) => (
          <TabLink key={candidate} href={href({ kind: "analytics", tab: candidate })} current={candidate === tab} testId={`analytics-tab-${candidate}`}>
            {ANALYTICS_TAB_LABELS[candidate]}
          </TabLink>
        ))}
        {extras.map((entry) => (
          <TabLink key={entry.id} href={href({ kind: "extension", id: entry.id, segment: entry.segment })} current={false} testId={`analytics-tab-${entry.id}`}>
            <FlaskConical className="size-3.5" aria-hidden="true" />
            {entry.label}
          </TabLink>
        ))}
      </nav>
      {tab === "events" ? (
        <EventsMetricsPage embedded />
      ) : (
        dashRef && <DashboardBody key={`${dashRef.kind}/${dashRef.key}`} dashRef={dashRef} />
      )}
    </Page>
  );
}

function TabLink({ href, current, testId, children }: { href: string; current: boolean; testId: string; children: React.ReactNode }) {
  return (
    <Link
      href={href}
      aria-current={current ? "page" : undefined}
      data-testid={testId}
      className={cn(
        "no-color -mb-px inline-flex h-9 shrink-0 items-center gap-1.5 border-b-2 px-3 text-ui transition-colors",
        current ? "border-primary font-medium text-ink" : "border-transparent text-ink-muted hover:text-ink"
      )}
    >
      {children}
    </Link>
  );
}
