// Copyright (c) 2025 Reliant Labs

/**
 * The pure rules of the delivery views: which environments a delivery request
 * covers, how a release comparison is linked, and how a change between its two
 * sides reads.
 *
 * ── THE SCOPE FOLLOWS THE READER ─────────────────────────────────────────────
 *
 * Release markers must describe the telemetry on screen, so the ledger request
 * takes the same org, project and environments as the charts. An attribute
 * filter on the environment (`deployment.environment.name is prod`, or the env
 * id) narrows the charts to it, so it narrows the markers too: it is mapped to
 * env ids through the environments the place knows. Every other filter (a
 * service, a version) means nothing to the ledger, which records promotions of
 * whole releases into environments, and is not sent.
 */

import { DEFAULT_HEALTH_WINDOW, HEALTH_WINDOWS, type DeliveryScope, type HealthWindow, type Promotion } from "@/services/controlPlane/observability/deliveryTypes";
import type { AttributeFilter, ObsScope } from "@/services/controlPlane/observability/types";

import type { ObsEnvironment } from "./obsPlace";

const ENV_NAME_KEY = "deployment.environment.name";
const ENV_ID_KEY = "reliant.env.id";

/**
 * The delivery scope of the telemetry scope on screen. Null when the filters
 * exclude every environment: there is nothing to ask the ledger for, and the
 * answer is "no releases", not "every environment's".
 */
export function deliveryScopeOf(scope: ObsScope, environments: readonly ObsEnvironment[]): DeliveryScope | null {
  const idOf = new Map(environments.map((env) => [env.name, env.id]));
  const ids = (filter: AttributeFilter): string[] => {
    const values = filter.op === "in" ? (filter.values ?? []) : [filter.value ?? ""];
    return filter.key === ENV_ID_KEY ? values : values.flatMap((name) => (idOf.has(name) ? [idOf.get(name)!] : []));
  };
  let envIds: string[] = [...scope.envIds];
  for (const filter of scope.filters) {
    if (filter.key !== ENV_NAME_KEY && filter.key !== ENV_ID_KEY) continue;
    if (filter.op === "eq" || filter.op === "in") {
      const wanted = ids(filter);
      envIds = envIds.length ? envIds.filter((id) => wanted.includes(id)) : wanted;
      if (envIds.length === 0) return null;
    } else if (filter.op === "neq") {
      const unwanted = ids(filter);
      const from = envIds.length ? envIds : environments.map((env) => env.id);
      // Without the place's environments there is no list to subtract from: leave it wide.
      if (from.length === 0) continue;
      envIds = from.filter((id) => !unwanted.includes(id));
      if (envIds.length === 0) return null;
    }
  }
  return { orgId: scope.orgId, projectId: scope.projectId, envIds: [...new Set(envIds)] };
}

// ── Release comparison ───────────────────────────────────────────────────────
//
// The windows themselves are the server's (GetReleaseHealth): it cuts each side
// at the neighbouring promotions and at now, so the console only names the
// width it asks for and renders what comes back.

export function healthWindowOf(param: string | undefined): HealthWindow {
  return (HEALTH_WINDOWS as readonly string[]).includes(param ?? "") ? (param as HealthWindow) : DEFAULT_HEALTH_WINDOW;
}

/**
 * The URL's `compare`: one promotion — `<envId>~<promotedAtMs>~<promotionId>`.
 * The id is what the server is asked about; the environment scopes the ask;
 * the moment lets a link say when without a round trip.
 */
export interface CompareRef {
  envId: string;
  promotedAtMs: number;
  promotionId: string;
}

export function compareParam(p: Pick<Promotion, "envId" | "promotedAtMs" | "promotionId">): string {
  return `${p.envId}~${Math.round(p.promotedAtMs)}~${p.promotionId}`;
}

export function parseCompareParam(param: string | undefined): CompareRef | null {
  if (!param) return null;
  const parts = param.split("~");
  if (parts.length < 3) return null;
  const promotionId = parts.pop()!;
  const at = Number(parts.pop());
  const envId = parts.join("~");
  if (!envId || !promotionId || !Number.isFinite(at) || at <= 0) return null;
  return { envId, promotedAtMs: at, promotionId };
}

/** How a figure moved, for colouring: which way, and whether that way is worse. */
export type Movement = "up-bad" | "up-good" | "down-bad" | "down-good" | "flat" | "none";

/**
 * Before → after for a figure where `higherIsWorse` (latency, error rate,
 * memory) or better (throughput is neither: pass `null` to only say which way).
 * A change within `tolerance` (relative) is flat: noise is not a finding.
 */
export function movement(before: number | null, after: number | null, higherIsWorse: boolean | null, tolerance = 0.1): Movement {
  if (before == null || after == null || !Number.isFinite(before) || !Number.isFinite(after)) return "none";
  const base = Math.abs(before);
  const change = after - before;
  if (base === 0 ? change === 0 : Math.abs(change) / base <= tolerance) return "flat";
  if (higherIsWorse === null) return change > 0 ? "up-good" : "down-good";
  if (change > 0) return higherIsWorse ? "up-bad" : "up-good";
  return higherIsWorse ? "down-good" : "down-bad";
}

/** "+42%", "−8%", or a percentage-point change for a figure that is itself a share. */
export function changeText(before: number | null, after: number | null, kind: "relative" | "points" = "relative"): string {
  if (before == null || after == null || !Number.isFinite(before) || !Number.isFinite(after)) return "";
  if (kind === "points") {
    const pts = (after - before) * 100;
    if (Math.abs(pts) < 0.05) return "±0 pts";
    return `${pts > 0 ? "+" : "−"}${Math.abs(pts).toFixed(Math.abs(pts) < 1 ? 2 : 1)} pts`;
  }
  if (before === 0) return after === 0 ? "±0%" : "new";
  const pct = ((after - before) / Math.abs(before)) * 100;
  if (Math.abs(pct) < 0.5) return "±0%";
  return `${pct > 0 ? "+" : "−"}${Math.round(Math.abs(pct))}%`;
}

// ── DORA figures, as the Releases page states them ───────────────────────────

/** "3h 12m", "2d 4h", "45m", "30s" — a span a person reads, for lead time and restore time. */
export function formatSpan(ms: number | null): string {
  if (ms == null || !Number.isFinite(ms)) return "—";
  const s = Math.round(ms / 1000);
  if (s < 60) return `${s}s`;
  const m = Math.round(s / 60);
  if (m < 60) return `${m}m`;
  const h = Math.floor(m / 60);
  if (h < 48) return m % 60 ? `${h}h ${m % 60}m` : `${h}h`;
  const d = Math.floor(h / 24);
  return h % 24 ? `${d}d ${h % 24}h` : `${d}d`;
}

/** Deployments per day as a reader says it: "2.4 a day", or "1 every 3.5 days" below one. */
export function formatPerDay(perDay: number): string {
  if (!Number.isFinite(perDay) || perDay <= 0) return "None";
  if (perDay >= 1) return `${perDay >= 10 ? Math.round(perDay) : Number(perDay.toFixed(1))} a day`;
  const every = 1 / perDay;
  return `1 every ${every >= 10 ? Math.round(every) : Number(every.toFixed(1))} days`;
}

/** The commit as it is usually read: the first 7 characters. */
export function shortCommit(commit: string): string {
  return /^[0-9a-f]{8,}$/i.test(commit) ? commit.slice(0, 7) : commit;
}
