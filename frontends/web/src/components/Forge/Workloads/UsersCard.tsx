// Copyright (c) 2026 Reliant Labs

/**
 * The Overview's Users card: who uses the app. Accounts, then the users
 * active, new and returning over the last 7 days, each against the 7 days
 * before, and daily active users over 30 days as a sparkline.
 *
 * It asks nothing of its own. Its figures ARE panels of the project's
 * generated Overview dashboard — forge's built-in Users section
 * (pkg/obsbuiltin: `active-7d`, `new-7d`, `returning-7d`, `accounts`,
 * `dau-30d`) — read through the same QueryPanel path the dashboard uses, so
 * the card and the dashboard (and the Users dashboard, whose figures compile
 * from the same forge fragments) cannot disagree, and no new RPC is needed.
 *
 * Hidden until there is something to show: a release whose Overview carries
 * no Users section (a forge before the built-in dashboards), a read that
 * failed or is still loading, or an app nobody has signed in to yet draws
 * nothing, so a project without users never sees an empty card.
 */

import { useMemo } from "react";
import { ArrowRight } from "lucide-react";

import Link from "@/components/forge-ui/link";
import { useDashboard, useDashboardIndex } from "@/hooks/dashboard-queries";
import { usePanelQuery } from "@/hooks/observability-queries";
import type { DashboardRef } from "@/services/controlPlane/observability/dashboardTypes";
import type { ObsScope, Panel as SpecPanel, PanelResult } from "@/services/controlPlane/observability/types";
import type { RangePreset } from "@/services/controlPlane/observability/window";
import { cn } from "@/lib/utils";

import { UPlotChart } from "../Observability/charts/UPlotChart";
import { describeChange } from "../Observability/Dashboards/PanelBody";
import { groupOf } from "../Observability/Dashboards/dashboardGroups";
import { CHART_PALETTE } from "../Observability/Dashboards/seriesModel";
import { formatCount } from "../Observability/format";
import { Panel } from "../Releases/releaseParts";

/** The Overview panels the card reads, by forge's ids. */
export const USERS_CARD_PANELS = { active: "active-7d", fresh: "new-7d", returning: "returning-7d", accounts: "accounts", daily: "dau-30d" } as const;

const NO_PANEL: SpecPanel = { id: "", title: "", kind: "", signal: "", series: [] };

export interface UsersCardProps {
  scope: ObsScope | null;
  project: string;
  range: RangePreset;
  /** The Users page (the identity provider's accounts). */
  usersHref?: string;
  /** A release dashboard's page, by key: "Open Users dashboard". */
  dashboardHref?: (key: string) => string;
}

/** A figure panel's first value and, when the statement returns its previous period beside it, that value too. */
export function figurePair(data: PanelResult | undefined): { now: number | null; before: number | null } {
  const values = data?.rows[0]?.values ?? [];
  return { now: values[0] ?? null, before: values.length > 1 ? (values[1] ?? null) : null };
}

export function UsersCard({ scope, project, range, usersHref, dashboardHref }: UsersCardProps) {
  const orgId = scope?.orgId ?? null;
  const index = useDashboardIndex(orgId, project, undefined);
  // The project's generated Overview: the one dashboard of the overview group the release shipped.
  const ref = useMemo<DashboardRef | null>(
    () => index.data?.dashboards.find((d) => d.source === "generated" && groupOf(d) === "overview")?.ref ?? null,
    [index.data]
  );
  const usersDashboard = index.data?.dashboards.find((d) => d.source === "generated" && d.ref.key === "users");
  const doc = useDashboard(orgId, ref);
  const panelOf = (id: string) => doc.data?.spec.panels.find((p) => p.id === id && p.kind === "sql");
  const panels = {
    active: panelOf(USERS_CARD_PANELS.active),
    fresh: panelOf(USERS_CARD_PANELS.fresh),
    returning: panelOf(USERS_CARD_PANELS.returning),
    accounts: panelOf(USERS_CARD_PANELS.accounts),
    daily: panelOf(USERS_CARD_PANELS.daily),
  };
  const dashboardId = doc.data?.spec.id;
  const options = (panel: SpecPanel | undefined) => ({ enabled: !!panel, dashboardId });
  const active = usePanelQuery(panels.active ? scope : null, range, panels.active ?? NO_PANEL, [], [], options(panels.active));
  const fresh = usePanelQuery(panels.fresh ? scope : null, range, panels.fresh ?? NO_PANEL, [], [], options(panels.fresh));
  const returning = usePanelQuery(panels.returning ? scope : null, range, panels.returning ?? NO_PANEL, [], [], options(panels.returning));
  const accounts = usePanelQuery(panels.accounts ? scope : null, range, panels.accounts ?? NO_PANEL, [], [], options(panels.accounts));
  const daily = usePanelQuery(panels.daily ? scope : null, range, panels.daily ?? NO_PANEL, [], [], options(panels.daily));

  // Nothing to show yet: no Users section in this release, or its figures have not answered.
  if (!panels.active || !panels.fresh || !panels.returning) return null;
  if (!active.data || !fresh.data || !returning.data) return null;
  const a = figurePair(active.data);
  const n = figurePair(fresh.data);
  const r = figurePair(returning.data);
  const total = figurePair(accounts.data).now;
  if (!a.now && !n.now && !total) return null;

  const dau = daily.data?.series.find((s) => Object.keys(s.group).length === 0);
  const rows: { label: string; value: { now: number | null; before: number | null }; testId: string }[] = [
    { label: "Active", value: a, testId: "overview-users-active" },
    { label: "New", value: n, testId: "overview-users-new" },
    { label: "Returning", value: r, testId: "overview-users-returning" },
  ];
  return (
    <Panel
      title="Users"
      meta={total !== null ? `${formatCount(total)} ${total === 1 ? "account" : "accounts"}` : undefined}
      actions={
        usersHref ? (
          <Link href={usersHref} className="no-color inline-flex h-7 items-center gap-1 rounded-md px-2 text-xs text-ink-muted hover:bg-ink/5 hover:text-ink" data-testid="overview-users-link">
            View users
            <ArrowRight className="h-3 w-3" aria-hidden="true" />
          </Link>
        ) : undefined
      }
      flush
      testId="overview-users"
    >
      <div className="px-4 pb-3 pt-2">
        <p className="mb-2 text-2xs uppercase tracking-wide text-ink-muted">Last 7 days</p>
        <dl className="grid grid-cols-3 gap-3">
          {rows.map((row) => {
            const change = describeChange(row.value.now, row.value.before, "count", "previous 7 days");
            return (
              <div key={row.label} className="min-w-0" data-testid={row.testId}>
                <dt className="text-xs text-ink-muted">{row.label}</dt>
                <dd className="text-lg font-semibold tabular-nums text-ink">{row.value.now === null ? "—" : formatCount(row.value.now)}</dd>
                {change && (
                  <dd className="truncate text-2xs tabular-nums text-ink-muted" title={change}>
                    {change.replace(/ vs previous 7 days$/, "")}
                  </dd>
                )}
              </div>
            );
          })}
        </dl>
        {dau && dau.values.some((v) => v !== null) && (
          <div className="mt-3" data-testid="overview-users-sparkline">
            <div className="flex items-baseline justify-between text-2xs text-ink-muted">
              <span>Daily active users, 30 days</span>
              <span className="tabular-nums">{formatCount(dau.values.filter((v): v is number => v !== null).at(-1) ?? 0)} so far today</span>
            </div>
            {/* The completed days only: today's count is still rising, and would read as a fall. */}
            <UPlotChart height={36} timestamps={dau.timestamps.slice(0, -1)} series={[{ label: "Daily active users", values: dau.values.slice(0, -1), color: CHART_PALETTE[0] }]} unit="count" label="Daily active users over the last 30 days" compact />
          </div>
        )}
        {dashboardHref && usersDashboard && (
          <Link
            href={dashboardHref(usersDashboard.ref.key)}
            className={cn("no-color mt-3 inline-flex items-center gap-1 text-xs text-ink-muted hover:text-ink")}
            data-testid="overview-users-dashboard"
          >
            Open Users dashboard
            <ArrowRight className="h-3 w-3" aria-hidden="true" />
          </Link>
        )}
      </div>
    </Panel>
  );
}
