// Copyright (c) 2025 Reliant Labs

/**
 * An in-memory DeliveryInsightsService: a 90-day release ledger for the
 * fixture app's three environments, and DORA computed over it by the same
 * rules control-plane applies (contract K5, "Definitions as implemented") —
 * so the markers on the charts, the promotions list and the metrics agree
 * with each other, and changing the failure window changes the numbers.
 *
 * The story it tells: releases go dev → staging → prod every week or so; prod
 * rolled v1.8.1 back to v1.8.0 three hours after promoting it; staging
 * re-applied v1.8.1 once; v1.8.3 reached staging three hours ago and prod 40
 * minutes ago, so the default "last hour" already has a marker on it. The
 * ledger records no commit time, so lead time is unknown — as it is for real.
 */

import { Code, ConnectError } from "@connectrpc/connect";

import type { DoraMetrics, Promotion, PromotionList, TimeToRestore } from "@/services/controlPlane/observability/deliveryTypes";
import type { ObservabilityApi } from "@/services/controlPlane/observability/ObservabilityApiContext";
import type { TimeWindow } from "@/services/controlPlane/observability/types";

const DEV = { id: "env-dev", name: "dev" };
const STAGING = { id: "env-staging", name: "staging" };
const PROD = { id: "env-prod", name: "prod" };
const ENVS = [DEV, STAGING, PROD];

const HOUR = 3_600_000;
const DAY = 24 * HOUR;

export interface DeliveryFixtureOptions {
  /**
   * "empty": nothing was ever promoted. "not-member": the caller is not in the
   * org. "env-denied": any request naming environments is refused, as one
   * naming another org's environment is.
   */
  delivery?: "ok" | "empty" | "not-member" | "env-denied";
  /** ListReleases' limit, overriding the request's: a small one shows truncation. */
  releaseLimit?: number;
  /** Some releases recorded a commit time (a future ledger): lead time partly measured. */
  leadTime?: "unknown" | "partial";
  /** Fill the reserved alert-based time to restore (A8), to show where it goes. */
  alertMttr?: boolean;
}

/** A stable 40-hex "commit" for a version. */
function commitOf(version: string): string {
  let h = 2166136261;
  let out = "";
  for (let round = 0; out.length < 40; round++) {
    for (const ch of `${version}#${round}`) h = Math.imul(h ^ ch.charCodeAt(0), 16777619) >>> 0;
    out += h.toString(16).padStart(8, "0");
  }
  return out.slice(0, 40);
}

/** The fixture's ledger, oldest first, relative to `t0`. */
export function fixtureLedger(t0: number): Promotion[] {
  const out: Promotion[] = [];
  let n = 0;
  const add = (env: { id: string; name: string }, version: string, agoMs: number, by: string, kind = "promote", tag = version) =>
    out.push({
      promotionId: `promo-${String(++n).padStart(3, "0")}`,
      envId: env.id,
      envName: env.name,
      releaseId: `rel-${version}`,
      version,
      gitCommit: commitOf(version),
      gitTag: tag,
      promotedAtMs: t0 - agoMs,
      promotedBy: by,
      promotedByUserId: by.startsWith("ci") || by.startsWith("import:") ? "" : `user-${by.split(" ")[0].toLowerCase()}`,
      kind,
    });
  // [version, staging days ago, prod days ago]
  const train: [string, number, number][] = [
    ["v1.6.0", 80, 79],
    ["v1.6.1", 72, 71.5],
    ["v1.7.0", 61, 60],
    ["v1.7.1", 52, 51.8],
    ["v1.7.2", 41, 40],
    ["v1.7.3", 33, 32],
    ["v1.8.0", 24, 23],
    ["v1.8.1", 10, 9],
    ["v1.8.2", 5, 4],
  ];
  for (const [version, staging, prod] of train) {
    const imported = staging > 75;
    add(DEV, version, (staging + 1) * DAY, imported ? "import:legacy-ledger" : "ci");
    add(STAGING, version, staging * DAY, imported ? "import:legacy-ledger" : "ci");
    add(PROD, version, prod * DAY, imported ? "import:legacy-ledger" : "Ada Lovelace");
    if (version === "v1.8.1") {
      // Rolled back three hours after it reached prod; staging re-applied it the next day.
      add(PROD, "v1.8.0", prod * DAY - 3 * HOUR, "Ada Lovelace", "rollback");
      add(STAGING, version, (staging - 1) * DAY, "Grace Hopper");
    }
  }
  add(DEV, "v1.8.3", 26 * HOUR, "ci");
  add(STAGING, "v1.8.3", 3 * HOUR, "ci");
  add(PROD, "v1.8.3", 40 * 60_000, "Ada Lovelace");
  add(DEV, "v1.8.4-rc.1", 20 * 60_000, "ci", "promote", "");
  return out.sort((a, b) => a.promotedAtMs - b.promotedAtMs);
}

/** The version running in `envId` at `atMs`: the last promotion before it. */
export function versionAt(ledger: readonly Promotion[], envId: string, atMs: number): string | null {
  return ledger.filter((p) => p.envId === envId && p.promotedAtMs <= atMs).at(-1)?.version ?? null;
}

type Classified = Promotion & { class: "deployment" | "rollback" | "reapply" };

/** contract K5: a rollback (recorded, or of a release this env ran before the running one), a re-apply, or a deployment. */
function classify(ledger: readonly Promotion[]): Classified[] {
  const out: Classified[] = [];
  for (const env of ENVS) {
    const ran: string[] = [];
    let running: string | null = null;
    for (const p of ledger.filter((q) => q.envId === env.id)) {
      const cls: Classified["class"] =
        p.kind === "rollback" ? "rollback" : p.releaseId === running ? "reapply" : running && ran.indexOf(p.releaseId) > -1 && ran.indexOf(p.releaseId) < ran.indexOf(running) ? "rollback" : "deployment";
      out.push({ ...p, class: cls });
      if (!ran.includes(p.releaseId)) ran.push(p.releaseId);
      running = p.releaseId;
    }
  }
  return out.sort((a, b) => a.promotedAtMs - b.promotedAtMs);
}

const median = (xs: number[]) => {
  if (!xs.length) return null;
  const s = [...xs].sort((a, b) => a - b);
  const m = Math.floor(s.length / 2);
  return s.length % 2 ? s[m] : (s[m - 1] + s[m]) / 2;
};
const mean = (xs: number[]) => (xs.length ? xs.reduce((a, b) => a + b, 0) / xs.length : null);

/** Go's time.Duration.String for whole hours, as the server formats N into its definitions. */
const goHours = (h: number) => `${h}h0m0s`;

export const DEFINITIONS = {
  deploymentFrequency:
    "Deployments per day: promotions in the window that put a different release in an environment, summed across the environments in scope and divided by the window length in days. A rollback (a promotion recorded as one, or of a release older than the one running) and a re-apply of the release already running are not deployments.",
  leadTime:
    "Time from the release's source commit time to its promotion (promoted_at), over the deployments in the window whose commit time is known. Deployments whose release recorded no commit time are counted as unknown and excluded; no value is inferred for them. A commit time later than the promotion is treated as unknown.",
  changeFailureRate: (h: number) =>
    `Share of deployments in the window that were rolled back within ${goHours(h)} of being promoted (a promotion recorded as a rollback, or of a release older than the one running). A deployment younger than ${goHours(h)} that has not been rolled back counts as not failed.`,
  timeToRestore: (h: number) =>
    `Time from a failed change's promotion to the rollback that restored the previous release, over the failed changes in the window (a failed change is one rolled back within ${goHours(h)} of being promoted). Measured from the release ledger; it does not use alerts.`,
};

/** DORA over `window` for `envIds` (empty: all), computed from the fixture ledger. */
export function fixtureDora(ledger: readonly Promotion[], envIds: string[], window: TimeWindow, failureHours: number, options: DeliveryFixtureOptions = {}): DoraMetrics {
  const inScope = (p: Promotion) => !envIds.length || envIds.includes(p.envId);
  const all = classify(ledger).filter(inScope);
  const inWindow = all.filter((p) => p.promotedAtMs >= window.startMs && p.promotedAtMs <= window.endMs);
  const deployments = inWindow.filter((p) => p.class === "deployment");
  const days = (window.endMs - window.startMs) / DAY;
  const n = failureHours * HOUR;
  const restores: number[] = [];
  let failed = 0;
  for (const d of deployments) {
    const next = all.find((p) => p.envId === d.envId && p.promotedAtMs > d.promotedAtMs);
    if (next && next.class === "rollback" && next.promotedAtMs - d.promotedAtMs <= n) {
      failed += 1;
      restores.push(next.promotedAtMs - d.promotedAtMs);
    }
  }
  const unknownReleases = new Set(deployments.map((d) => d.releaseId)).size;
  const partial = options.leadTime === "partial" && deployments.length > 2;
  const measured = partial ? deployments.slice(-2).map((_, i) => (i + 2) * 26 * HOUR + 17 * 60_000) : [];
  const ttr: TimeToRestore = {
    definition: DEFINITIONS.timeToRestore(failureHours),
    source: "release_ledger",
    failures: failed,
    medianMs: median(restores),
    meanMs: mean(restores),
  };
  return {
    windowStartMs: window.startMs,
    windowEndMs: window.endMs,
    failureWindowHours: failureHours,
    environmentCount: envIds.length || ENVS.length,
    deploymentFrequency: {
      definition: DEFINITIONS.deploymentFrequency,
      deployments: deployments.length,
      perDay: days > 0 ? deployments.length / days : 0,
      rollbacks: inWindow.filter((p) => p.class === "rollback").length,
      reapplies: inWindow.filter((p) => p.class === "reapply").length,
    },
    leadTime: {
      definition: DEFINITIONS.leadTime,
      measuredChanges: measured.length,
      unknownChanges: deployments.length - measured.length,
      unknownReleases: partial ? Math.max(0, unknownReleases - 2) : unknownReleases,
      medianMs: median(measured),
      meanMs: mean(measured),
    },
    changeFailureRate: {
      definition: DEFINITIONS.changeFailureRate(failureHours),
      changes: deployments.length,
      failedChanges: failed,
      rate: deployments.length ? failed / deployments.length : null,
    },
    timeToRestore: ttr,
    alertTimeToRestore: options.alertMttr
      ? {
          definition: "Time from an alert firing in an environment to it resolving, over the alerts in the window. Measured from alert history.",
          source: "alerts",
          failures: 4,
          medianMs: 38 * 60_000,
          meanMs: 52 * 60_000,
        }
      : null,
  };
}

type DeliveryApi = Pick<ObservabilityApi, "listReleases" | "getDoraMetrics">;

export function createDeliveryFixture(
  options: DeliveryFixtureOptions,
  call: <T>(rpc: string, request: unknown, answer: () => T) => Promise<T>,
  now: () => number = Date.now
): DeliveryApi & { ledger: Promotion[] } {
  const mode = options.delivery ?? "ok";
  const ledger = mode === "empty" ? [] : fixtureLedger(now());
  const gate = <T>(rpc: string, request: { envIds: string[] }, answer: () => T) =>
    call(rpc, request, () => {
      if (mode === "not-member") throw new ConnectError("not a member of this organization", Code.PermissionDenied);
      if (mode === "env-denied" && request.envIds.length) {
        throw new ConnectError("one or more environments are not available to this organization", Code.PermissionDenied);
      }
      return answer();
    });
  return {
    ledger,
    listReleases: (req) =>
      gate("ListReleases", req, (): PromotionList => {
        const limit = options.releaseLimit ?? req.limit ?? 500;
        const hits = ledger.filter(
          (p) => (!req.envIds.length || req.envIds.includes(p.envId)) && p.promotedAtMs >= req.window.startMs && p.promotedAtMs <= req.window.endMs
        );
        return { promotions: hits.slice(-limit), truncated: hits.length > limit };
      }),
    getDoraMetrics: (req) =>
      gate("GetDoraMetrics", req, () => {
        const end = now();
        const window = req.window ?? { startMs: end - 30 * DAY, endMs: end };
        return fixtureDora(ledger, req.envIds, window, req.failureWindowHours || 24, options);
      }),
  };
}
