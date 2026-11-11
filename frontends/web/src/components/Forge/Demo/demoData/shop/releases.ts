// Copyright (c) 2025 Reliant Labs

/**
 * The Releases dashboard's sql panels over shop's story (forge's generated
 * `releases` dashboard, forge-dashboards.json): adoption, releases side by
 * side, crash-free sessions and new issues by release. Every figure comes
 * from the release ledger (demoLedger: which release each environment ran,
 * and when) and the traffic model (demoStats), the same answers the
 * Releases page, the overview and the Health tab read, so the dashboard
 * agrees with them: prod's v1.8.3 is the release whose error rate and p95
 * departed, v1.8.2 before it held, and staging's rollback is in the ledger.
 *
 * A panel is answered by its id (the forge spec's): a statement the demo
 * does not know is answered empty, as before.
 */

import type { Panel, PanelResult, QueryPanelRequest, TimeWindow } from "@/services/controlPlane/observability/types";

import { DAY, DEMO_ENV_IDS, DEMO_INCIDENT, demoLedger, demoStats, incidentStartMs, type DemoEnvKey } from "../shopStory";
import { shopIssueFirstSeen } from "./issues";

/** A version's run in one environment: from its promotion to the next whole-release one (or now). */
interface Run {
  envId: DemoEnvKey;
  version: string;
  startMs: number;
  endMs: number;
}

/** The runs in `envIds` that overlap `window`, clipped to it. */
export function runsIn(envIds: readonly DemoEnvKey[], window: TimeWindow, now: number): Run[] {
  const out: Run[] = [];
  for (const envId of envIds) {
    const whole = demoLedger(now).filter((p) => p.envId === envId && !p.targets.length);
    whole.forEach((p, i) => {
      const startMs = Math.max(window.startMs, p.atMs);
      const endMs = Math.min(window.endMs, whole[i + 1]?.atMs ?? now, now);
      if (endMs > startMs) out.push({ envId, version: p.version, startMs, endMs });
    });
  }
  return out;
}

const envsOf = (req: Pick<QueryPanelRequest, "envIds">): DemoEnvKey[] =>
  (req.envIds.length ? DEMO_ENV_IDS.filter((e) => req.envIds.includes(e)) : [...DEMO_ENV_IDS]) as DemoEnvKey[];

/** "2026-11-04 09:12", UTC: the statements' formatDateTime. */
const utc = (ms: number) => new Date(ms).toISOString().slice(0, 16).replace("T", " ");

/** shop-web sessions in a window: one per ~2 product views (the storefront's entry), as the sessions model reads them. */
const sessionsIn = (envId: DemoEnvKey, w: TimeWindow, now: number) =>
  Math.round(demoStats({ ...w, envIds: [envId], service: "api", op: "GET /products/{slug}", now }).requests * 0.5);

/** Sessions that hit an error: only the incident's failed checkouts (prod, v1.8.3, since its promotion). */
function erroredSessionsIn(run: Run, now: number): number {
  if (run.envId !== DEMO_INCIDENT.envId || run.version !== DEMO_INCIDENT.release) return 0;
  const startMs = Math.max(run.startMs, incidentStartMs(now));
  if (run.endMs <= startMs) return 0;
  return demoStats({ startMs, endMs: run.endMs, envIds: [run.envId], service: "api", op: "checkout.v1.CheckoutService/Checkout", now }).errors;
}

/** A bucket width that draws ~42 points over the window: 4 hours over a week. */
function stepOf(window: TimeWindow, asked: number): number {
  if (asked > 0) return asked;
  const span = (window.endMs - window.startMs) / 1000;
  return [300, 900, 3600, 4 * 3600, 6 * 3600].find((s) => span / s <= 48) ?? 86_400;
}

/** Each bucket's share per version of `measure` over the runs: one series per version, a fraction (unit percent). */
function shareSeries(runs: Run[], window: TimeWindow, stepSec: number, label: string, measure: (run: Run, w: TimeWindow) => number) {
  const step = stepSec * 1000;
  const first = Math.floor(window.startMs / step) * step;
  const timestamps: number[] = [];
  for (let t = first; t < window.endMs; t += step) timestamps.push(t / 1000);
  const versions = [...new Set(runs.map((r) => r.version))];
  const byVersion = new Map(versions.map((v) => [v, timestamps.map(() => 0)]));
  timestamps.forEach((t, i) => {
    const bucket = { startMs: Math.max(window.startMs, t * 1000), endMs: Math.min(window.endMs, t * 1000 + step) };
    for (const run of runs) {
      const w = { startMs: Math.max(bucket.startMs, run.startMs), endMs: Math.min(bucket.endMs, run.endMs) };
      if (w.endMs > w.startMs) byVersion.get(run.version)![i] += measure(run, w);
    }
  });
  const totals = timestamps.map((_, i) => versions.reduce((a, v) => a + byVersion.get(v)![i], 0));
  return versions.map((version) => ({
    label,
    group: { Version: version },
    timestamps,
    values: byVersion.get(version)!.map((n, i) => (totals[i] ? Math.round((n / totals[i]) * 10_000) / 10_000 : null)),
    value: null,
  }));
}

/** Per version over the window: its first and last minute served, and RED summed over the environments in scope. */
export function versionRows(runs: Run[], now: number) {
  const versions = [...new Set(runs.map((r) => r.version))];
  return versions
    .map((version) => {
      const mine = runs.filter((r) => r.version === version);
      const stats = mine.map((r) => demoStats({ startMs: r.startMs, endMs: r.endMs, envIds: [r.envId], now }));
      const requests = stats.reduce((a, s) => a + s.requests, 0);
      const errors = stats.reduce((a, s) => a + s.errors, 0);
      const q = (k: "p50Ms" | "p95Ms" | "p99Ms") => (requests ? Math.round((stats.reduce((a, s) => a + s[k] * s.requests, 0) / requests) * 10) / 10 : null);
      return {
        version,
        firstMs: Math.min(...mine.map((r) => r.startMs)),
        lastMs: Math.max(...mine.map((r) => r.endMs)),
        requests,
        errorRate: requests ? Math.round((10_000 * errors) / requests) / 100 : null,
        p50: q("p50Ms"),
        p95: q("p95Ms"),
        p99: q("p99Ms"),
      };
    })
    .sort((a, b) => b.firstMs - a.firstMs);
}

/**
 * The answer to one of the Releases dashboard's sql panels, or null for a
 * statement this file does not answer. `base` is the empty result the caller
 * builds (its kind, unit and window).
 */
export function shopReleasesSqlPanel(panel: Panel, req: QueryPanelRequest, now: number, base: PanelResult): PanelResult | null {
  const window = { startMs: req.window.startMs, endMs: Math.min(req.window.endMs, now) };
  const envs = envsOf(req);
  const runs = runsIn(envs, window, now);
  switch (panel.id) {
    case "backend-version-share": {
      const stepSeconds = stepOf(window, req.stepSeconds);
      const series = shareSeries(runs, window, stepSeconds, "Share of requests", (run, w) => demoStats({ ...w, envIds: [run.envId], now }).requests);
      return { ...base, stepSeconds, bucketSeconds: stepSeconds, series };
    }
    case "browser-version-share": {
      const stepSeconds = stepOf(window, req.stepSeconds);
      const series = shareSeries(runs, window, stepSeconds, "Share of sessions", (run, w) => sessionsIn(run.envId, w, now));
      return { ...base, stepSeconds, bucketSeconds: stepSeconds, series };
    }
    case "version-comparison":
      return {
        ...base,
        columns: ["Requests", "Error rate (%)", "p50 (ms)", "p95 (ms)", "p99 (ms)"],
        rows: versionRows(runs, now).map((r) => ({
          group: { Version: r.version, "First seen (UTC)": utc(r.firstMs), "Last seen (UTC)": utc(r.lastMs) },
          values: [r.requests, r.errorRate, r.p50, r.p95, r.p99],
        })),
      };
    case "crash-free-sessions-by-version": {
      const versions = [...new Set(runs.map((r) => r.version))];
      const rows = versions
        .map((version) => {
          const mine = runs.filter((r) => r.version === version);
          const sessions = mine.reduce((a, r) => a + sessionsIn(r.envId, r, now), 0);
          const errored = Math.min(sessions, mine.reduce((a, r) => a + erroredSessionsIn(r, now), 0));
          return { version, firstMs: Math.min(...mine.map((r) => r.startMs)), sessions, errored };
        })
        .filter((r) => r.sessions > 0)
        .sort((a, b) => b.firstMs - a.firstMs);
      return {
        ...base,
        columns: ["Sessions", "Sessions with an error", "Crash-free sessions (%)"],
        rows: rows.map((r) => ({
          group: { Version: r.version, "First session (UTC)": utc(r.firstMs) },
          values: [r.sessions, r.errored, Math.round((10_000 * (r.sessions - r.errored)) / r.sessions) / 100],
        })),
      };
    }
    case "new-issues-by-release": {
      const counts = new Map<string, { n: number; firstMs: number }>();
      for (const issue of shopIssueFirstSeen(now)) {
        if (!envs.includes(issue.envId as DemoEnvKey) || issue.firstSeenMs < window.startMs || issue.firstSeenMs >= window.endMs) continue;
        const c = counts.get(issue.firstVersion) ?? { n: 0, firstMs: issue.firstSeenMs };
        counts.set(issue.firstVersion, { n: c.n + 1, firstMs: Math.min(c.firstMs, issue.firstSeenMs) });
      }
      return {
        ...base,
        columns: ["New issues"],
        rows: [...counts.entries()].sort((a, b) => a[1].firstMs - b[1].firstMs).map(([version, c]) => ({ group: { Version: version }, values: [c.n] })),
      };
    }
    default:
      return null;
  }
}

/** The window a 7-day Releases dashboard opens on, for tests. */
export const releasesWindow = (now: number): TimeWindow => ({ startMs: now - 7 * DAY, endMs: now });
