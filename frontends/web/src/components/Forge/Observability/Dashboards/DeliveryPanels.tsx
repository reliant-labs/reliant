// Copyright (c) 2025 Reliant Labs

/**
 * The delivery kinds of forge's dashspec (pkg/dashspec delivery.go): panels
 * drawn from the release ledger and the release-health verdict rather than
 * from telemetry, so they carry no query of their own. Each kind is one read
 * the console already makes:
 *
 *   release_health  GetReleaseHealth for each environment's running release:
 *                   the scorecard. The verdict, findings and windows are the
 *                   server's (forge's pkg/releasehealth, `forge obs check`'s
 *                   rule), so this panel, a release's Health tab and the CLI
 *                   say the same thing. It links to the Health tab for the
 *                   full comparison.
 *   promotions      ListReleases over the range: one lane per environment
 *                   (dev → staging → prod), a mark per promotion, rollbacks
 *                   called out, then the rows newest first.
 *   delivery        GetDoraMetrics over the range: the four DORA figures,
 *                   each with the definition that produced it.
 *
 * They follow the environments in scope (every one of the project when none
 * is selected); a service filter does not narrow them.
 */

import { useCallback, useRef, useState, type ReactNode } from "react";
import { ArrowRight, CheckCircle2, CircleHelp, RotateCcw, XCircle } from "lucide-react";

import Link from "@/components/forge-ui/link";
import Mono from "@/components/forge-ui/mono";
import Tag from "@/components/forge-ui/tag";
import { useCurrentReleaseHealths, useDoraMetrics, useReleases } from "@/hooks/observability-queries";
import { DEFAULT_HEALTH_WINDOW, HEALTH_WINDOW_MS, type DeliveryScope, type DoraMetrics, type Promotion, type ReleaseHealth, type ReleaseVerdict, type ServiceWindowStats } from "@/services/controlPlane/observability/deliveryTypes";
import { obsErrorMessage } from "@/services/controlPlane/observability/errors";
import { resolveWindow, type RangePreset } from "@/services/controlPlane/observability/window";
import { cn } from "@/lib/utils";

import { chartColor } from "../charts/chartPalette";
import { formatAgo, formatCount, formatDuration, formatPercent } from "../format";
import type { ObsEnvironment } from "../obsPlace";
import { findingLabel } from "../ReleaseComparison";

/** The kinds this file draws. */
export const DELIVERY_KINDS: ReadonlySet<string> = new Set(["release_health", "promotions", "delivery"]);

export function isDeliveryKind(kind: string): boolean {
  return DELIVERY_KINDS.has(kind);
}

/** What a delivery panel reads: the dashboard's delivery scope, its environments and range, and where a release opens. */
export interface DeliveryPanelContext {
  scope: DeliveryScope | null;
  environments: readonly ObsEnvironment[];
  range: RangePreset;
  /** A release's page; `extra` is its search (`{ tab: "health", in: "prod" }`). */
  releaseHref?: (version: string, extra?: Record<string, unknown>) => string;
}

const DAY_MS = 86_400_000;
/** DORA's failure window: a change fails when a rollback follows it within a day (GetDoraMetrics' default). */
const FAILURE_HOURS = 24;

/** The lanes in delivery order: dev, then staging, then prod, then any other environment by name. */
const LANE_RANK: Record<string, number> = { dev: 0, development: 0, staging: 1, stage: 1, preprod: 2, prod: 3, production: 3 };
export function laneOrder<T extends { name: string }>(envs: readonly T[]): T[] {
  return [...envs].sort((a, b) => (LANE_RANK[a.name] ?? 2.5) - (LANE_RANK[b.name] ?? 2.5) || a.name.localeCompare(b.name));
}

/** The environments a delivery panel covers: those in scope, else every one of the project. */
function envsInScope(context: DeliveryPanelContext): ObsEnvironment[] {
  const ids = context.scope?.envIds ?? [];
  const envs = ids.length ? context.environments.filter((env) => ids.includes(env.id)) : [...context.environments];
  return laneOrder(envs);
}

export function DeliveryPanelBody({ kind, limit, context }: { kind: string; limit?: number; context?: DeliveryPanelContext }) {
  if (!context?.scope) {
    return <DeliveryNote testId="delivery-panel-no-scope">Choose a project environment to read its releases.</DeliveryNote>;
  }
  switch (kind) {
    case "release_health":
      return <ReleaseHealthPanel context={context} />;
    case "promotions":
      return <PromotionsPanel context={context} limit={limit} />;
    case "delivery":
      return <DeliveryMetricsPanel context={context} />;
    default:
      return null;
  }
}

function DeliveryNote({ children, testId }: { children: ReactNode; testId: string }) {
  return (
    <p className="py-4 text-center text-xs text-ink-muted" data-testid={testId}>
      {children}
    </p>
  );
}

function Loading({ label }: { label: string }) {
  return <div className="h-16 animate-pulse rounded bg-ink/5 motion-reduce:animate-none" aria-busy="true" aria-label={label} />;
}

// ── release_health ───────────────────────────────────────────────────────────

const VERDICT: Record<ReleaseVerdict, { label: string; tone: string; icon: ReactNode }> = {
  pass: { label: "Good", tone: "text-success-ink", icon: <CheckCircle2 className="h-4 w-4" aria-hidden="true" /> },
  fail: { label: "Regressed", tone: "text-danger-ink", icon: <XCircle className="h-4 w-4" aria-hidden="true" /> },
  inconclusive: { label: "Not enough data yet", tone: "text-warning-ink", icon: <CircleHelp className="h-4 w-4" aria-hidden="true" /> },
  unknown: { label: "No verdict", tone: "text-ink-muted", icon: <CircleHelp className="h-4 w-4" aria-hidden="true" /> },
};

/** One side of a release for the scorecard's service: its requests, their rate per minute, error rate and p95. */
export interface ScoreSide {
  requests: number;
  perMinute: number;
  errorRate: number;
  p95Ms: number;
}

export interface Scorecard {
  /** The service the figures are: the one a failing finding names, else the busiest after the promotion. */
  service: string;
  before: ScoreSide | null;
  after: ScoreSide | null;
}

/**
 * The scorecard's figures from a GetReleaseHealth answer: one service's
 * before and after, as release health judges each service on its own, so the
 * figures and the findings beside them agree. Nothing is judged here: the
 * verdict is the server's.
 */
export function scorecardOf(h: ReleaseHealth): Scorecard {
  const failing = h.findings.find((f) => f.policy === "fail" && f.service)?.service;
  const busiest = [...h.services].sort((a, b) => (b.after?.requests ?? 0) - (a.after?.requests ?? 0))[0];
  const chosen = h.services.find((s) => s.service === failing) ?? busiest;
  const side = (stats: ServiceWindowStats | null | undefined, window: ReleaseHealth["before"]): ScoreSide | null => {
    if (!stats || !window) return null;
    const minutes = Math.max(1, (window.endMs - window.startMs) / 60_000);
    return { requests: stats.requests, perMinute: stats.requests / minutes, errorRate: stats.errorRate, p95Ms: stats.p95Ms };
  };
  return { service: chosen?.service ?? "", before: side(chosen?.before, h.before), after: side(chosen?.after, h.after) };
}

function ReleaseHealthPanel({ context }: { context: DeliveryPanelContext }) {
  const envs = envsInScope(context);
  const checks = useCurrentReleaseHealths(context.scope, envs.map((env) => env.id), HEALTH_WINDOW_MS[DEFAULT_HEALTH_WINDOW]);
  if (!envs.length) return <DeliveryNote testId="release-health-no-envs">This project has no environments yet.</DeliveryNote>;
  return (
    <div className="grid gap-3 md:grid-cols-2 xl:grid-cols-3" data-testid="release-health-panel">
      {envs.map((env) => {
        const check = checks.get(env.id);
        return <ReleaseHealthCard key={env.id} env={env} check={check} releaseHref={context.releaseHref} />;
      })}
    </div>
  );
}

function ReleaseHealthCard({
  env,
  check,
  releaseHref,
}: {
  env: ObsEnvironment;
  check: { data?: ReleaseHealth; isLoading: boolean; error: unknown } | undefined;
  releaseHref?: DeliveryPanelContext["releaseHref"];
}) {
  const frame = (children: ReactNode, verdict?: ReleaseVerdict) => (
    <section
      className={cn("min-w-0 rounded-md border border-border bg-surface-sunken p-3", verdict === "fail" && "border-danger/40")}
      aria-label={`${env.name} release health`}
      data-testid={`release-health-${env.name}`}
      data-verdict={verdict}
    >
      {children}
    </section>
  );
  if (!check || check.isLoading) return frame(<Loading label={`Loading ${env.name}'s release health`} />);
  if (check.error || !check.data) {
    const missing = /not found|no promotion|no release/i.test(obsErrorMessage(check.error));
    return frame(
      <>
        <p className="text-sm font-medium text-ink">{env.name}</p>
        <p className="mt-1 text-xs text-ink-muted">{missing ? "No release promoted here yet." : `Release health is unavailable: ${obsErrorMessage(check.error)}`}</p>
      </>
    );
  }
  const h = check.data;
  const verdict = VERDICT[h.verdict] ?? VERDICT.unknown;
  const card = scorecardOf(h);
  const version = h.promotion?.version ?? "";
  const previous = h.promotion?.previousVersion ?? "";
  const failing = h.findings.filter((f) => f.policy === "fail");
  const shown = (failing.length ? failing : h.findings).slice(0, 2);
  return frame(
    <>
      <div className="flex items-start justify-between gap-2">
        <div className="min-w-0">
          <p className="text-xs font-medium text-ink-muted">{env.name}</p>
          <p className="truncate text-sm font-medium text-ink">
            {version ? <Mono value={version} copy={false} /> : "—"}
            {previous && (
              <span className="text-xs font-normal text-ink-muted">
                {" "}
                vs <Mono value={previous} copy={false} />
              </span>
            )}
          </p>
          {h.promotion && <p className="text-xs text-ink-muted">promoted {formatAgo(h.promotion.promotedAtMs)}</p>}
        </div>
        <span className={cn("inline-flex shrink-0 items-center gap-1 text-xs font-medium", verdict.tone)} data-testid="release-health-verdict">
          {verdict.icon}
          {verdict.label}
        </span>
      </div>
      <dl className="mt-3 grid grid-cols-2 gap-x-3 gap-y-2 text-xs">
        <Figure label={card.service ? `Error rate · ${card.service}` : "Error rate"} before={card.before && formatPercent(card.before.errorRate)} after={card.after && formatPercent(card.after.errorRate)} worse={!!card.before && !!card.after && card.after.errorRate > card.before.errorRate} />
        <Figure label="p95 latency" before={card.before && formatDuration(card.before.p95Ms)} after={card.after && formatDuration(card.after.p95Ms)} worse={!!card.before && !!card.after && card.after.p95Ms > card.before.p95Ms * 1.1} />
        <Figure label="Throughput" before={card.before && formatThroughput(card.before.perMinute)} after={card.after && formatThroughput(card.after.perMinute)} />
        <div className="min-w-0">
          <dt className="text-ink-muted">Issues</dt>
          <dd className={cn("tabular-nums text-ink", h.newIssues.length + h.regressedIssues.length > 0 && "text-danger-ink")}>
            {h.newIssues.length} new · {h.regressedIssues.length} regressed
          </dd>
        </div>
      </dl>
      {h.verdict === "inconclusive" && !h.afterComplete && <p className="mt-2 text-xs text-ink-muted">The after window is still open: the verdict can change as traffic arrives.</p>}
      {shown.length > 0 && (
        <ul className="mt-2 space-y-1 text-xs">
          {shown.map((f, i) => (
            <li key={`${f.code}-${i}`} className={cn("truncate", f.policy === "fail" ? "text-danger-ink" : "text-ink-muted")} title={f.message}>
              <span className="font-medium">{findingLabel(f.code ?? "")}:</span> {f.message}
            </li>
          ))}
        </ul>
      )}
      {version && releaseHref && (
        <Link href={releaseHref(version, { tab: "health", in: env.name })} className="mt-2 inline-flex items-center gap-1 text-xs text-primary-ink" data-testid="release-health-open">
          Open the Health tab <ArrowRight className="h-3 w-3" aria-hidden="true" />
        </Link>
      )}
    </>,
    h.verdict
  );
}

function Figure({ label, before, after, worse = false }: { label: string; before: string | null; after: string | null; worse?: boolean }) {
  return (
    <div className="min-w-0">
      <dt className="truncate text-ink-muted">{label}</dt>
      <dd className="tabular-nums text-ink">
        <span className="text-ink-muted">{before ?? "—"}</span> → <span className={cn(worse && "text-danger-ink")}>{after ?? "—"}</span>
      </dd>
    </div>
  );
}

// ── promotions ───────────────────────────────────────────────────────────────

function PromotionsPanel({ context, limit }: { context: DeliveryPanelContext; limit?: number }) {
  const max = limit && limit > 0 ? limit : 50;
  const releases = useReleases(context.scope, `range:${context.range}`, () => resolveWindow(context.range), { limit: max });
  if (releases.isLoading) return <Loading label="Loading promotions" />;
  if (releases.error || !releases.data) {
    return (
      <p role="alert" className="text-xs text-danger-ink" data-testid="promotions-error">
        Promotions couldn&apos;t be loaded. {obsErrorMessage(releases.error)}
      </p>
    );
  }
  const promotions = releases.data.promotions;
  if (!promotions.length) return <DeliveryNote testId="promotions-empty">No promotions in this window.</DeliveryNote>;
  const window = resolveWindow(context.range);
  const named = new Map(context.environments.map((env) => [env.id, env]));
  const lanes = laneOrder([...new Set(promotions.map((p) => p.envId))].map((id) => ({ id, name: named.get(id)?.name ?? promotions.find((p) => p.envId === id)?.envName ?? id })));
  const newest = [...promotions].reverse();
  return (
    <div className="space-y-3" data-testid="promotions-panel">
      <PromotionLanes lanes={lanes} promotions={promotions} window={window} />
      <div className="overflow-x-auto">
        <table className="w-full min-w-[34rem] text-left text-xs">
          <thead className="text-ink-muted">
            <tr>
              <th className="py-1 pr-3 font-medium">When</th>
              <th className="py-1 pr-3 font-medium">Environment</th>
              <th className="py-1 pr-3 font-medium">Release</th>
              <th className="py-1 pr-3 font-medium">Commit</th>
              <th className="py-1 font-medium">By</th>
            </tr>
          </thead>
          <tbody>
            {newest.map((p) => (
              <tr key={p.promotionId} className="border-t border-border/60" data-testid={`promotion-row-${p.promotionId}`} data-kind={p.kind}>
                <td className="py-1.5 pr-3 tabular-nums text-ink-muted" title={new Date(p.promotedAtMs).toISOString()}>
                  {formatAgo(p.promotedAtMs)}
                </td>
                <td className="py-1.5 pr-3 text-ink">{p.envName}</td>
                <td className="py-1.5 pr-3 text-ink">
                  <span className="inline-flex items-center gap-1.5">
                    {context.releaseHref ? (
                      <Link href={context.releaseHref(p.version, { in: p.envName })} className="text-primary-ink">
                        <Mono value={p.version} copy={false} />
                      </Link>
                    ) : (
                      <Mono value={p.version} copy={false} />
                    )}
                    {p.kind === "rollback" && (
                      <Tag tone="warning" icon={<RotateCcw className="h-3 w-3" aria-hidden="true" />}>
                        rollback
                      </Tag>
                    )}
                  </span>
                </td>
                <td className="py-1.5 pr-3 text-ink-muted">{p.gitCommit ? <Mono value={p.gitCommit} length={7} /> : "—"}</td>
                <td className="py-1.5 text-ink">{p.promotedBy || "—"}</td>
              </tr>
            ))}
          </tbody>
        </table>
      </div>
      {releases.data.truncated && <p className="text-xs text-ink-muted">Showing the newest {max}.</p>}
    </div>
  );
}

/** A version label's width in px (font-mono text-xs, "v1.12.3"-sized, its padding and the dot beside it). */
const LABEL_PX = 64;

/**
 * Where each of a lane's marks puts its version label, in px from the lane's
 * left edge, or null for a dot drawn alone. A label sits right of its dot, or
 * left of it where the lane ends. The newest is always named (the release
 * running now), then every rollback (what the lane is read for), then the
 * rest newest first; a label is kept only where it overlaps none kept.
 */
export function placeLabels(xs: readonly number[], laneWidth: number, labelWidth = LABEL_PX, rollbacks: readonly boolean[] = []): (number | null)[] {
  const out: (number | null)[] = xs.map(() => null);
  const kept: [number, number][] = [];
  const newestFirst = xs.map((_, i) => i).reverse();
  const order = [...newestFirst.slice(0, 1), ...newestFirst.slice(1).filter((i) => rollbacks[i]), ...newestFirst.slice(1).filter((i) => !rollbacks[i])];
  for (const i of order) {
    const left = xs[i] + 9 + labelWidth <= laneWidth ? xs[i] + 9 : Math.max(0, xs[i] - 9 - labelWidth);
    const right = left + labelWidth;
    if (kept.every(([l, r]) => right + 2 <= l || left >= r + 2)) {
      out[i] = left;
      kept.push([left, right]);
    }
  }
  return out;
}

/** The width of an element, kept current as it resizes. */
function useWidth<T extends HTMLElement>(): [(el: T | null) => void, number] {
  const [width, setWidth] = useState(0);
  const observer = useRef<ResizeObserver | null>(null);
  const ref = useCallback((el: T | null) => {
    observer.current?.disconnect();
    if (!el) return;
    setWidth(el.getBoundingClientRect().width);
    if (typeof ResizeObserver === "undefined") return;
    observer.current = new ResizeObserver(([entry]) => setWidth(entry.contentRect.width));
    observer.current.observe(el);
  }, []);
  return [ref, width];
}

/** One lane per environment across the range, a dot per promotion: a release's path from dev to prod reads left to right. */
function PromotionLanes({ lanes, promotions, window }: { lanes: { id: string; name: string }[]; promotions: Promotion[]; window: { startMs: number; endMs: number } }) {
  const [laneRef, laneWidth] = useWidth<HTMLDivElement>();
  const span = Math.max(1, window.endMs - window.startMs);
  const xOf = (ms: number) => Math.min(1, Math.max(0, (ms - window.startMs) / span)) * laneWidth;
  return (
    <div className="rounded-md border border-border/60 bg-surface-sunken px-3 py-2" role="img" aria-label={`Promotions over the range: ${promotions.length} across ${lanes.map((l) => l.name).join(", ")}`} data-testid="promotion-lanes">
      {lanes.map((lane, index) => {
        const mine = promotions.filter((p) => p.envId === lane.id);
        const xs = mine.map((p) => xOf(p.promotedAtMs));
        const labels = placeLabels(xs, laneWidth, LABEL_PX, mine.map((p) => p.kind === "rollback"));
        return (
          <div key={lane.id} className="flex items-center gap-3 py-1.5">
            <span className="w-14 shrink-0 truncate text-xs text-ink-muted">{lane.name}</span>
            <div ref={index === 0 ? laneRef : undefined} className="relative h-6 flex-1">
              <div className="absolute inset-x-0 top-1/2 h-px bg-border" aria-hidden="true" />
              {mine.map((p, i) => {
                const rollback = p.kind === "rollback";
                return (
                  <span
                    key={p.promotionId}
                    className={cn("absolute top-1/2 h-2.5 w-2.5 -translate-x-1/2 -translate-y-1/2 rounded-full border-2 border-surface", rollback && "bg-warning")}
                    style={{ left: xs[i], background: rollback ? undefined : chartColor(0) }}
                    title={`${p.version} to ${p.envName} ${formatAgo(p.promotedAtMs)}${p.promotedBy ? ` by ${p.promotedBy}` : ""}${rollback ? " (rollback)" : ""}`}
                    data-testid={`promotion-mark-${p.promotionId}`}
                  />
                );
              })}
              {mine.map((p, i) =>
                labels[i] === null ? null : (
                  <span
                    key={`label-${p.promotionId}`}
                    className={cn("absolute top-1/2 z-10 -translate-y-1/2 whitespace-nowrap rounded bg-surface-sunken px-1 font-mono text-xs leading-4", p.kind === "rollback" ? "text-warning-ink" : "text-ink")}
                    style={{ left: labels[i] as number }}
                    aria-hidden="true"
                  >
                    {p.kind === "rollback" ? "↺ " : ""}
                    {p.version}
                  </span>
                )
              )}
            </div>
          </div>
        );
      })}
    </div>
  );
}

// ── delivery (DORA) ──────────────────────────────────────────────────────────

function DeliveryMetricsPanel({ context }: { context: DeliveryPanelContext }) {
  const window = resolveWindow(context.range);
  const days = Math.max(1, Math.round((window.endMs - window.startMs) / DAY_MS));
  const dora = useDoraMetrics(context.scope, days, FAILURE_HOURS);
  if (dora.isLoading) return <Loading label="Loading delivery figures" />;
  if (dora.error || !dora.data) {
    return (
      <p role="alert" className="text-xs text-danger-ink" data-testid="delivery-error">
        Delivery figures couldn&apos;t be loaded. {obsErrorMessage(dora.error)}
      </p>
    );
  }
  return <DoraFigures dora={dora.data} />;
}

export function DoraFigures({ dora }: { dora: DoraMetrics }) {
  const { deploymentFrequency: df, leadTime: lt, changeFailureRate: cfr, timeToRestore: ttr } = dora;
  const perWeek = df.perDay * 7;
  return (
    <dl className="grid grid-cols-2 gap-3 xl:grid-cols-4" data-testid="delivery-panel">
      <Stat
        label="Deployment frequency"
        value={df.perDay >= 1 ? `${trimmed(df.perDay)}/day` : `${trimmed(perWeek)}/week`}
        detail={`${df.deployments} deployment${df.deployments === 1 ? "" : "s"}${df.rollbacks ? ` · ${df.rollbacks} rollback${df.rollbacks === 1 ? "" : "s"}` : ""}`}
        definition={df.definition}
      />
      <Stat
        label="Lead time"
        value={lt.medianMs === null ? "—" : formatSpan(lt.medianMs)}
        detail={lt.measuredChanges ? `median, commit to promotion · ${lt.measuredChanges} measured` : "no commit times recorded"}
        definition={lt.definition}
      />
      <Stat
        label="Change failure rate"
        value={cfr.rate === null ? "—" : formatPercent(cfr.rate)}
        detail={`${cfr.failedChanges} of ${cfr.changes} rolled back within ${dora.failureWindowHours}h`}
        definition={cfr.definition}
        warn={!!cfr.rate}
      />
      <Stat
        label="Time to restore"
        value={ttr.medianMs === null ? "—" : formatSpan(ttr.medianMs)}
        detail={ttr.failures ? `median, failed change to rollback · ${ttr.failures} restored` : "nothing to restore"}
        definition={ttr.definition}
      />
    </dl>
  );
}

function Stat({ label, value, detail, definition, warn = false }: { label: string; value: string; detail: string; definition: string; warn?: boolean }) {
  return (
    <div className="min-w-0 rounded-md border border-border/60 bg-surface-sunken px-3 py-2" title={definition} data-testid={`dora-${label.toLowerCase().replace(/ /g, "-")}`}>
      <dt className="truncate text-xs text-ink-muted">{label}</dt>
      <dd className={cn("mt-0.5 text-xl font-semibold tabular-nums text-ink", warn && "text-warning-ink")}>{value}</dd>
      <dd className="truncate text-xs text-ink-muted">{detail}</dd>
    </div>
  );
}

/** Requests per minute, or per hour for a quiet service, so a low rate never reads as zero. */
export function formatThroughput(perMinute: number): string {
  if (perMinute >= 1) return `${formatCount(Math.round(perMinute))}/min`;
  if (perMinute * 60 >= 1) return `${formatCount(Math.round(perMinute * 60))}/h`;
  return perMinute > 0 ? "<1/h" : "0/min";
}

const trimmed = (n: number) => (n >= 10 ? String(Math.round(n)) : n.toFixed(1).replace(/\.0$/, ""));

/** A span a reader says aloud: minutes, hours or days. */
export function formatSpan(ms: number): string {
  if (ms < 60 * 60_000) return formatDuration(ms);
  if (ms < 2 * DAY_MS) return `${trimmed(ms / 3_600_000)}h`;
  return `${trimmed(ms / DAY_MS)}d`;
}
