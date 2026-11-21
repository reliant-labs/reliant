// Copyright (c) 2026 Reliant Labs

/**
 * PRODUCT ANALYTICS › EVENTS & METRICS (U6b round 3, prototype):
 * /forge/p/:app/events. Read-only.
 *
 *   events    the custom product events the app sends (`track()`, stored as
 *             `product.<name>` in product_events): one QueryPanel SQL read,
 *             grouped by event, with its count, distinct users and last seen.
 *   metrics   the app's own OpenTelemetry metrics (ListMetrics, declared or
 *             custom; forge's built-ins and infrastructure left out).
 *
 * Each links to explore: an event to the metrics explorer's SQL tab, a metric
 * to the explorer charted.
 */

import { useMemo } from "react";

import Link from "@/components/forge-ui/link";
import Tag from "@/components/forge-ui/tag";
import { LearnLink } from "@/lib/learnLinks";
import { useMetricCatalog, useObservabilityOrg, usePanelQuery } from "@/hooks/observability-queries";
import type { ObsScope, Panel } from "@/services/controlPlane/observability/types";

import { useForgeConsole } from "../Console/consoleContext";
import { Header, Page } from "../Console/consolePageFrame";
import { ENV_ALL } from "../Console/envScope";
import { PAGE_SUBTITLES } from "../Console/groupedNav";
import { chString } from "../Observability/Dashboards/alertExpr";
import { formatCount } from "../Observability/format";
import { useExploreHref } from "../Observability/Metrics/pivots";
import { sqlPanelOf } from "../Observability/Metrics/model/explorerState";
import { customMetrics, metricPanel } from "../Services/servicesModel";

const RANGE = "7d" as const;

export const EVENTS_PANEL: Panel = {
  id: "product-events-catalog",
  title: "Product events",
  kind: "sql",
  signal: "",
  series: [],
  sql:
    "SELECT EventName AS event, count() AS events, uniqExact(UserId) AS users, toUnixTimestamp(max(Timestamp)) AS last_seen FROM product_events " +
    "WHERE Timestamp >= {from:DateTime64(9)} AND Timestamp < {to:DateTime64(9)} GROUP BY event ORDER BY events DESC LIMIT 100",
};

/**
 * The explorer SQL for one event's count over time. The event name is data
 * (any string an app passed to track()), so it is written as an exact
 * ClickHouse string literal: a SQL panel takes no caller-supplied parameters,
 * only the scope's {step}, {from} and {to}.
 */
export function eventExploreSql(event: string): string {
  return (
    "SELECT toStartOfInterval(Timestamp, toIntervalSecond({step:UInt32})) AS t, count() AS events FROM product_events " +
    `WHERE EventName = ${chString(event)} AND Timestamp >= {from:DateTime64(9)} AND Timestamp < {to:DateTime64(9)} GROUP BY t ORDER BY t`
  );
}

const TH = "h-9 px-3 text-left text-meta font-medium text-ink-muted whitespace-nowrap";
const TD = "h-11 px-3 text-ui tabular-nums whitespace-nowrap";

/** `embedded`: a tab of the Analytics page (U6c), which carries the page title. */
export function EventsMetricsPage({ embedded = false }: { embedded?: boolean } = {}) {
  const { forgeProject, env, roster } = useForgeConsole();
  const org = useObservabilityOrg();
  const envId = env === ENV_ALL ? undefined : roster.envs.find((candidate) => candidate.name === env)?.live?.id;
  const scope = useMemo<ObsScope | null>(() => (org.orgId ? { orgId: org.orgId, projectId: forgeProject, envIds: envId ? [envId] : [], filters: [] } : null), [org.orgId, forgeProject, envId]);
  const events = usePanelQuery(scope, RANGE, EVENTS_PANEL, []);
  const catalog = useMetricCatalog(scope, "24h");
  const metrics = useMemo(() => customMetrics(catalog.data?.metrics ?? []), [catalog.data]);
  const exploreHref = useExploreHref();

  const rows = useMemo(() => {
    const result = events.data;
    if (!result) return [];
    const cols = result.columns;
    const at = (name: string) => cols.indexOf(name);
    return result.rows.map((row) => {
      const values = row.values;
      const num = (name: string) => {
        const i = at(name);
        return i >= 0 ? values[i] ?? null : null;
      };
      // String columns come back in the row's group; numbers in values, by column.
      return { event: row.group.event ?? Object.values(row.group)[0] ?? "", events: num("events"), users: num("users"), lastSeen: num("last_seen") };
    });
  }, [events.data]);
  const custom = rows.filter((row) => row.event.startsWith("product."));

  return (
    <Page testId="console-events">
      {!embedded && <Header title="Events & metrics" subtitle={PAGE_SUBTITLES.events} />}

      <section className="space-y-3" data-testid="events-list">
        <h2 className="text-section text-ink">
          Product events <span className="ml-2 text-meta font-normal text-ink-muted">sent with track(), last 7 days</span>
        </h2>
        {events.isLoading ? (
          <div className="h-32 animate-pulse rounded-xl border border-border bg-surface motion-reduce:animate-none" />
        ) : events.error && !events.data ? (
          <p className="text-ui text-ink-muted">Product events could not be read right now.</p>
        ) : custom.length === 0 ? (
          <p className="rounded-xl border border-dashed border-border px-4 py-5 text-ui text-ink-muted">
            No product event in the last 7 days. Call <code className="font-mono text-code">track(&quot;signup&quot;)</code> in the app (forge&apos;s web runtime or{" "}
            <code className="font-mono text-code">observe.Track</code> in Go) and each event appears here as <code className="font-mono text-code">product.signup</code>.{" "}
            <LearnLink section="analytics" label="How product events work" className="align-baseline" />
          </p>
        ) : (
          <div className="overflow-x-auto rounded-xl border border-border bg-surface">
            <table className="w-full border-collapse">
              <thead>
                <tr className="border-b border-border">
                  <th className={TH}>Event</th>
                  <th className={`${TH} text-right`}>Events</th>
                  <th className={`${TH} text-right`}>Users</th>
                  <th className={`${TH} text-right`}>Last seen</th>
                  <th className={TH} />
                </tr>
              </thead>
              <tbody>
                {custom.map((row) => {
                  const explore = exploreHref?.({ panel: { ...sqlPanelOf(eventExploreSql(row.event)), title: row.event } }, { range: RANGE }) ?? null;
                  return (
                    <tr key={row.event} className="border-b border-border-subtle last:border-b-0" data-testid={`event-row-${row.event}`}>
                      <td className={`${TD} font-mono text-code text-ink`}>{row.event}</td>
                      <td className={`${TD} text-right`}>{row.events === null ? "—" : formatCount(row.events)}</td>
                      <td className={`${TD} text-right text-ink-muted`}>{row.users === null ? "—" : formatCount(row.users)}</td>
                      <td className={`${TD} text-right text-ink-muted`}>{row.lastSeen ? ago(row.lastSeen * 1000) : "—"}</td>
                      <td className={`${TD} text-right`}>
                        {explore && (
                          <Link href={explore} className="no-color text-meta text-primary-ink hover:underline">
                            Explore →
                          </Link>
                        )}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </section>

      <section className="space-y-3 pt-4" data-testid="metrics-list">
        <h2 className="text-section text-ink">
          Custom metrics <span className="ml-2 text-meta font-normal text-ink-muted">your services&apos; own OpenTelemetry metrics, last 24 hours</span>
        </h2>
        {catalog.isLoading ? (
          <div className="h-32 animate-pulse rounded-xl border border-border bg-surface motion-reduce:animate-none" />
        ) : metrics.length === 0 ? (
          <p className="rounded-xl border border-dashed border-border px-4 py-5 text-ui text-ink-muted">No custom metric yet. Forge&apos;s built-in request and runtime metrics are under More tools › Metrics.</p>
        ) : (
          <div className="overflow-x-auto rounded-xl border border-border bg-surface">
            <table className="w-full border-collapse">
              <thead>
                <tr className="border-b border-border">
                  <th className={TH}>Metric</th>
                  <th className={TH}>Kind</th>
                  <th className={TH}>Services</th>
                  <th className={TH}>About</th>
                  <th className={TH} />
                </tr>
              </thead>
              <tbody>
                {metrics.map((m) => {
                  const explore = exploreHref?.({ panel: metricPanel(m) }, { range: "24h" }) ?? null;
                  return (
                    <tr key={m.name} className="border-b border-border-subtle last:border-b-0" data-testid={`metric-row-${m.name}`}>
                      <td className={`${TD} font-mono text-code text-ink`}>{m.name}</td>
                      <td className={`${TD} text-ink-muted`}>
                        {m.kind}
                        {m.unit ? ` · ${m.unit}` : ""}
                      </td>
                      <td className={TD}>
                        <span className="flex gap-1">
                          {m.services.slice(0, 3).map((s) => (
                            <Tag key={s}>{s}</Tag>
                          ))}
                        </span>
                      </td>
                      <td className={`${TD} max-w-80 truncate text-ink-muted`} title={m.description}>
                        {m.description || (m.declared ? "Declared in the telemetry schema" : "")}
                      </td>
                      <td className={`${TD} text-right`}>
                        {explore && (
                          <Link href={explore} className="no-color text-meta text-primary-ink hover:underline">
                            Explore →
                          </Link>
                        )}
                      </td>
                    </tr>
                  );
                })}
              </tbody>
            </table>
          </div>
        )}
      </section>
    </Page>
  );
}

function ago(ms: number): string {
  const minutes = Math.max(0, Math.round((Date.now() - ms) / 60_000));
  if (minutes < 60) return `${minutes}m ago`;
  const hours = Math.round(minutes / 60);
  return hours < 48 ? `${hours}h ago` : `${Math.round(hours / 24)}d ago`;
}
