// Copyright (c) 2026 Reliant Labs

/**
 * A static site's hosting, below its domains (Site insights R3, design D4):
 *
 *   traffic        requests, cache hit ratio, bandwidth and 4xx/5xx rates over
 *                  24h / 7d / 30d (GetStaticSiteTraffic), on the service
 *                  page's chart cards
 *   usage          the site's storage, the org's storage and the org's transfer
 *                  this month against the free allowance, with this site's
 *                  share and the reset; a billing link when capped
 *   configuration  runtime config (public: config.js is served to every
 *                  visitor), base path, asset directory, what an unknown path
 *                  gets, and the _headers / _redirects rule counts
 *
 * Usage and configuration are GetStaticSite's; each section reads on its own,
 * so one that fails leaves the others standing.
 */

import { useState, type ReactNode } from "react";
import { AlertTriangle, Info } from "lucide-react";

import Link from "@/components/forge-ui/link";
import ProgressBar from "@/components/forge-ui/progress_bar";
import Tooltip from "@/components/forge-ui/tooltip";
import type { StaticSiteConfiguration } from "@/gen/controlplane/services/static_site/v1/static_site_pb";
import { cn } from "@/lib/utils";

import { Segmented } from "../Observability/Dashboards/parts";
import { formatCount, formatPercent } from "../Observability/format";
import { MetricCard, MetricLine, Section } from "./serviceParts";
import type { SiteEnv } from "./servicesModel";
import {
  capNote,
  basePathLabel,
  DEFAULT_TRAFFIC_RANGE,
  formatBinaryBytes,
  HIT_RATIO_NOTE,
  historyNote,
  HOSTING_BILLING_HREF,
  notFoundModeCopy,
  ruleCount,
  runtimeConfigEntries,
  TRAFFIC_RANGE_LABEL,
  TRAFFIC_RANGES,
  usageView,
  type MeterState,
  type TrafficRange,
  type UsageMeter,
} from "./staticSiteModel";
import { hostingReadProblem, useStaticSite, useStaticSiteTraffic, type StaticSiteTarget } from "./useStaticSite";

const targetOf = (name: string, site: SiteEnv): StaticSiteTarget | null => (site.envId ? { envId: site.envId, frontend: name } : null);

/** What a section says when it has nothing to show. */
function Quiet({ testId, children }: { testId?: string; children: ReactNode }) {
  return (
    <p className="rounded-xl border border-dashed border-border px-4 py-5 text-ui text-ink-muted" data-testid={testId}>
      {children}
    </p>
  );
}

function BillingLink({ testId }: { testId: string }) {
  return (
    <Link href={HOSTING_BILLING_HREF} className="no-color text-primary-ink hover:underline" data-testid={testId}>
      Set up billing →
    </Link>
  );
}

// ── Traffic ─────────────────────────────────────────────────────────────────

export function SiteTraffic({ name, site }: { name: string; site: SiteEnv }) {
  const [range, setRange] = useState<TrafficRange>(DEFAULT_TRAFFIC_RANGE);
  const target = targetOf(name, site);
  const traffic = useStaticSiteTraffic(target, range);
  const view = traffic.data;
  const loading = traffic.isLoading;
  const problem = hostingReadProblem(traffic.error, "traffic");
  const empty = view?.empty ? `${name} answered no request in this range.` : view && view.series.timestamps.length < 2 ? "Too few points in this range to draw." : null;
  const note = view ? historyNote(view) : null;
  return (
    <Section
      title="Traffic"
      meta={`in ${site.envName}`}
      actions={
        <Segmented label="Range" value={range} options={TRAFFIC_RANGES.map((value) => ({ value, label: TRAFFIC_RANGE_LABEL[value] }))} onChange={setRange} testId="site-traffic-range" />
      }
      testId="site-traffic"
    >
      {!target ? (
        <Quiet>The console does not list {site.envName}, so its traffic cannot be read here.</Quiet>
      ) : traffic.error && !view ? (
        <Quiet testId="site-traffic-unavailable">{problem ?? "Traffic could not be read right now."}</Quiet>
      ) : (
        <>
          <div className="grid gap-4 md:grid-cols-2" data-testid="site-traffic-charts">
            <MetricCard title="Requests" figure={view ? formatCount(view.totals.requests) : "—"} loading={loading} empty={empty} testId="site-traffic-requests">
              {view && <MetricLine timestamps={view.series.timestamps} values={view.series.requests} unit="count" label={`Requests to ${name}`} />}
            </MetricCard>
            <MetricCard
              title={
                <span className="inline-flex items-center gap-1">
                  Cache hit ratio
                  <Tooltip content={HIT_RATIO_NOTE} tooltipClassName="max-w-xs">
                    <Info className="size-3.5" aria-label="How the hit ratio is counted" data-testid="site-traffic-hit-ratio-note" />
                  </Tooltip>
                </span>
              }
              figure={view?.totals.hitRatio == null ? "—" : formatPercent(view.totals.hitRatio)}
              loading={loading}
              empty={empty}
              testId="site-traffic-hit-ratio"
            >
              {view && <MetricLine timestamps={view.series.timestamps} values={view.series.hitRatio} unit="percent" label={`Cache hit ratio of ${name}`} />}
            </MetricCard>
            <MetricCard title="Bandwidth" figure={view ? formatBinaryBytes(view.totals.bytes) : "—"} detail="traffic telemetry, not the bill" loading={loading} empty={empty} testId="site-traffic-bandwidth">
              {view && <MetricLine timestamps={view.series.timestamps} values={view.series.bytes} unit="bytes" format={formatBinaryBytes} label={`Bytes sent by ${name}`} />}
            </MetricCard>
            <MetricCard
              title="Errors"
              figure={view?.totals.rate5xx == null ? "—" : `${formatPercent(view.totals.rate5xx)} 5xx`}
              detail={view?.totals.rate4xx == null ? undefined : `${formatPercent(view.totals.rate4xx)} 4xx · ${formatCount(view.totals.requests4xx + view.totals.requests5xx)} failed`}
              tone={view?.totals.rate5xx != null && view.totals.rate5xx >= 0.05 ? "warning" : undefined}
              loading={loading}
              empty={empty}
              testId="site-traffic-errors"
            >
              {view && (
                <MetricLine
                  timestamps={view.series.timestamps}
                  unit="percent"
                  label={`Error rates of ${name}`}
                  series={[
                    { label: "4xx", values: view.series.rate4xx, tone: "warning" },
                    { label: "5xx", values: view.series.rate5xx, tone: "destructive" },
                  ]}
                />
              )}
            </MetricCard>
          </div>
          {note && (
            <p className="text-meta text-ink-muted" data-testid="site-traffic-history">
              {note} <BillingLink testId="site-traffic-upgrade" />
            </p>
          )}
        </>
      )}
    </Section>
  );
}

// ── Usage and free tier ─────────────────────────────────────────────────────

const METER_VARIANT: Record<MeterState, "default" | "warning" | "danger"> = { uncapped: "default", under: "default", near: "warning", over: "danger" };

export function SiteUsage({ name, site }: { name: string; site: SiteEnv }) {
  const target = targetOf(name, site);
  const query = useStaticSite(target);
  const view = query.data?.usage ? usageView(query.data.usage) : null;
  const note = view ? capNote(view) : null;
  return (
    <Section title="Usage and free tier" testId="site-usage">
      {!target ? (
        <Quiet>The console does not list {site.envName}, so its usage cannot be read here.</Quiet>
      ) : query.isLoading ? (
        <div className="h-24 animate-pulse rounded-xl border border-border bg-surface motion-reduce:animate-none" aria-label="Loading usage" />
      ) : !view ? (
        <Quiet testId="site-usage-unavailable">{hostingReadProblem(query.error, "site") ?? "Usage could not be read right now."}</Quiet>
      ) : (
        <div className="space-y-3">
          <p className="text-ui text-ink-muted" data-testid="site-usage-summary">
            {view.summary}
          </p>
          <div className="grid gap-4 rounded-xl border border-border bg-surface px-4 py-3 sm:grid-cols-3">
            {view.meters.map((meter) => (
              <Meter key={meter.id} meter={meter} />
            ))}
          </div>
          {(note || view.billing) && (
            <p className="flex items-start gap-2 text-ui text-ink" data-testid="site-usage-cap">
              <AlertTriangle className={cn("mt-0.5 size-4 shrink-0", view.billing ? "text-danger-ink" : "text-warning-ink")} aria-hidden="true" />
              <span>
                {note} {view.billing && <BillingLink testId="site-usage-billing" />}
              </span>
            </p>
          )}
        </div>
      )}
    </Section>
  );
}

function Meter({ meter }: { meter: UsageMeter }) {
  return (
    <div className="min-w-0 space-y-1.5" data-testid={`site-usage-${meter.id}`} data-state={meter.state}>
      <div className="flex flex-wrap items-baseline justify-between gap-x-3">
        <span className="text-ui font-medium text-ink">{meter.label}</span>
        <span className="text-ui tabular-nums text-ink-muted">
          <span className="text-ink">{meter.usedLabel}</span>
          {meter.limitLabel && ` ${meter.limitLabel}`}
        </span>
      </div>
      {meter.limit > 0 && meter.used !== null && <ProgressBar value={meter.used} max={meter.limit} variant={METER_VARIANT[meter.state]} size="sm" ariaLabel={`${meter.label} against its cap`} />}
      {meter.note && <p className="text-meta text-ink-muted">{meter.note}</p>}
    </div>
  );
}

// ── Configuration ───────────────────────────────────────────────────────────

export function SiteConfiguration({ name, site }: { name: string; site: SiteEnv }) {
  const target = targetOf(name, site);
  const query = useStaticSite(target);
  const config = query.data?.configuration;
  return (
    <Section title="Configuration" meta={`in ${site.envName}`} testId="site-configuration">
      {!target ? (
        <Quiet>The console does not list {site.envName}, so its configuration cannot be read here.</Quiet>
      ) : query.isLoading ? (
        <div className="h-24 animate-pulse rounded-xl border border-border bg-surface motion-reduce:animate-none" aria-label="Loading configuration" />
      ) : !config ? (
        <Quiet testId="site-configuration-unavailable">{hostingReadProblem(query.error, "site") ?? "The configuration could not be read right now."}</Quiet>
      ) : (
        <SiteConfigurationList config={config} />
      )}
    </Section>
  );
}

/** The configuration list itself (exported for its copy's tests). */
export function SiteConfigurationList({ config }: { config: StaticSiteConfiguration }) {
  const notFound = notFoundModeCopy(config.notFoundMode);
  const entries = runtimeConfigEntries(config);
  return (
    <dl className="divide-y divide-border-subtle rounded-xl border border-border bg-surface text-ui" data-testid="site-configuration-list">
      <Row label="Runtime config" testId="site-config-runtime">
        {entries.length === 0 ? (
          <span className="text-ink-muted">None</span>
        ) : (
          <div className="space-y-1">
            <ul className="space-y-0.5">
              {entries.map((entry) => (
                <li key={entry.key} className="flex min-w-0 flex-wrap gap-x-2 font-mono text-code">
                  <span className="text-ink">{entry.key}</span>
                  <span className="min-w-0 break-all text-ink-muted">{entry.workloadUrl ? `URL of ${entry.value}` : `= ${entry.value}`}</span>
                </li>
              ))}
            </ul>
            <p className="text-meta text-ink-muted">Public: config.js is served to every visitor, so keep secrets out of it.</p>
          </div>
        )}
      </Row>
      <Row label="Base path" testId="site-config-base-path">
        <span className="font-mono text-code text-ink">{basePathLabel(config.basePath)}</span>
      </Row>
      <Row label="Asset directory" testId="site-config-asset-dir">
        {config.assetDir ? <span className="font-mono text-code text-ink">{config.assetDir}</span> : <span className="text-ink-muted">None declared</span>}
      </Row>
      <Row label="Unknown paths" testId="site-config-not-found">
        <div className="space-y-0.5">
          <div className="font-medium text-ink">{notFound.label}</div>
          <p className="text-meta text-ink-muted">{notFound.explainer}</p>
        </div>
      </Row>
      <Row label="_headers" testId="site-config-headers">
        <span className="text-ink">{ruleCount(config.headersRules, "rule")}</span>
      </Row>
      <Row label="_redirects" testId="site-config-redirects">
        <span className="text-ink">{ruleCount(config.redirectRules, "rule")}</span>
      </Row>
    </dl>
  );
}

function Row({ label, testId, children }: { label: string; testId: string; children: ReactNode }) {
  return (
    <div className="grid gap-1 px-4 py-2.5 sm:grid-cols-[10rem_1fr] sm:gap-4" data-testid={testId}>
      <dt className="font-medium text-ink-muted">{label}</dt>
      <dd className="min-w-0">{children}</dd>
    </div>
  );
}
