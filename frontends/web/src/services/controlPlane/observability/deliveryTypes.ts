// Copyright (c) 2025 Reliant Labs

/**
 * The console's model of control-plane DeliveryInsightsService (contract K5):
 * the release ledger as promotions, and the DORA metrics computed over it.
 *
 * Like ./types.ts these are not the wire types: ./deliveryApi.ts is the one
 * file that maps the wire into these shapes. Times are epoch milliseconds,
 * durations milliseconds. A figure the server leaves unset is `null` here,
 * never 0: an unmeasured lead time is unknown, not instant.
 */

import type { Issue } from "./issuesTypes";
import type { TimeWindow } from "./types";

/**
 * Who and what a delivery request covers. Wire-compatible with the telemetry
 * scope for the shared fields: the org, optionally one project, and the
 * environments (empty: every environment of the org, or of the project).
 * Services and attribute filters mean nothing to the ledger and are not sent.
 */
export interface DeliveryScope {
  orgId: string;
  projectId?: string;
  envIds: string[];
}

/** One promotion of a release into one environment. A release promoted to three environments is three. */
export interface Promotion {
  promotionId: string;
  envId: string;
  envName: string;
  releaseId: string;
  version: string;
  /** The commit the release was built from; may be empty for an imported ledger. */
  gitCommit: string;
  gitTag: string;
  promotedAtMs: number;
  /** The user's name (email when unnamed), the automation actor, or `import:<name>`. */
  promotedBy: string;
  promotedByUserId: string;
  /** "promote" or "rollback", as the ledger records it. */
  kind: string;
}

export interface PromotionList {
  /** Oldest first. */
  promotions: Promotion[];
  /** The range held more than the limit: these are the newest `limit`. */
  truncated: boolean;
}

/** Releases per day, and what was promoted but did not count. */
export interface DeploymentFrequency {
  definition: string;
  deployments: number;
  perDay: number;
  rollbacks: number;
  reapplies: number;
}

/**
 * Commit → promotion. The ledger records no commit time today, so every
 * deployment is unknown and `medianMs` / `meanMs` are null.
 */
export interface LeadTime {
  definition: string;
  measuredChanges: number;
  unknownChanges: number;
  unknownReleases: number;
  medianMs: number | null;
  meanMs: number | null;
}

export interface ChangeFailureRate {
  definition: string;
  changes: number;
  failedChanges: number;
  /** Null when there were no deployments. */
  rate: number | null;
}

export interface TimeToRestore {
  definition: string;
  /** "release_ledger" for rollback-based MTTR; "alert_events" for the alert-based one. */
  source: string;
  /** Restored incidents: the ones the median and mean cover. */
  failures: number;
  /** Alert-based only: incidents that started in the window and have not resolved. In neither median nor mean; always 0 for the ledger. */
  ongoing: number;
  medianMs: number | null;
  meanMs: number | null;
}

export interface DoraMetrics {
  windowStartMs: number;
  windowEndMs: number;
  failureWindowHours: number;
  environmentCount: number;
  deploymentFrequency: DeploymentFrequency;
  leadTime: LeadTime;
  changeFailureRate: ChangeFailureRate;
  /** Failed change → the rollback that restored service, from the release ledger. */
  timeToRestore: TimeToRestore;
  /**
   * Alert → resolution, from alert history (A8), a separate measurement from
   * the ledger's. Null when the server leaves field 9 unset (alert history not
   * wired, or unreadable): "no alert-based figure", never "zero incidents" —
   * a set figure with zero counts means alert history was read and nothing fired.
   */
  alertTimeToRestore: TimeToRestore | null;
}

/** ListReleases' own bounds: the newest `limit` (max 2000) over at most 366 days. */
export const RELEASES_LIMIT = { default: 500, max: 2000 } as const;
export const DELIVERY_MAX_RANGE_DAYS = 366;

// ── Release health (ObservabilityService.GetReleaseHealth) ────────────────────

/**
 * "Did this release make things worse", as the server judges it. The windows,
 * the thresholds, the findings and the verdict are control-plane's (forge's
 * pkg/releasehealth): the console renders them and computes none of them, so
 * it agrees with `forge obs check` and with alerting by construction.
 */
export type ReleaseVerdict = "pass" | "fail" | "inconclusive" | "unknown";

/** The finding codes, as forge's pkg/release spells them (and `not_evaluated` lists them). */
export const RELEASE_FINDING_CODES = ["error_rate", "latency", "new_issue", "regressed_issue", "alert_firing", "workload_restarts"] as const;
export type ReleaseFindingCode = (typeof RELEASE_FINDING_CODES)[number];

/** The widths a health check offers on each side: the server's default is 15 minutes, its maximum 6 hours. */
export const HEALTH_WINDOWS = ["15m", "30m", "1h", "6h"] as const;
export type HealthWindow = (typeof HEALTH_WINDOWS)[number];
export const DEFAULT_HEALTH_WINDOW: HealthWindow = "15m";
export const HEALTH_WINDOW_MS: Record<HealthWindow, number> = {
  "15m": 15 * 60_000,
  "30m": 30 * 60_000,
  "1h": 60 * 60_000,
  "6h": 6 * 60 * 60_000,
};

export interface ReleasePromotionRef {
  promotionId: string;
  version: string;
  gitCommit: string;
  promotedAtMs: number;
  /** The release it replaced; empty for the first. */
  previousVersion: string;
}

export interface ServiceWindowStats {
  requests: number;
  /** A fraction, 0..1. */
  errorRate: number;
  p50Ms: number;
  p95Ms: number;
  p99Ms: number;
}

/** One service before and after, over weighted server spans (tail sampling corrected for). */
export interface ServiceHealthDelta {
  service: string;
  before: ServiceWindowStats | null;
  after: ServiceWindowStats | null;
  /** The share (0..1) of after-window traffic the judged version served: rollout progress. */
  versionShare: number;
}

export interface ReleaseAlertRef {
  rule: string;
  fingerprint: string;
  firingAtMs: number;
  labels: Record<string, string>;
}

/** A restart or OOM kill during the after window. */
export interface WorkloadFinding {
  workload: string;
  restarts: number;
  lastTerminatedReason: string;
}

export interface ReleaseFinding {
  /** Null for a code this console does not know (a newer server). */
  code: ReleaseFindingCode | null;
  /** Empty for a finding that is not about one service. */
  service: string;
  /** A fraction for error_rate, a ratio for latency, a count for the rest. */
  value: number;
  threshold: number;
  policy: "fail" | "warn" | string;
  /** One line a person or an agent can act on. */
  message: string;
}

export interface EffectiveThreshold {
  name: string;
  value: string;
  /** "release_shape" (declared in KCL), "default" or "request". */
  source: string;
}

export interface ReleaseHealth {
  promotion: ReleasePromotionRef | null;
  before: TimeWindow | null;
  after: TimeWindow | null;
  /** False while the after window is still open. */
  afterComplete: boolean;
  services: ServiceHealthDelta[];
  newIssues: Issue[];
  regressedIssues: Issue[];
  firingAlerts: ReleaseAlertRef[];
  workloadFindings: WorkloadFinding[];
  verdict: ReleaseVerdict;
  findings: ReleaseFinding[];
  thresholds: EffectiveThreshold[];
  /** Finding codes this server could not check, so a PASS does not cover them. Raw tokens, unknown ones kept. */
  notEvaluated: string[];
}

export interface ReleaseHealthRequest extends DeliveryScope {
  /** Which promotion; absent: the environment's current one. */
  promotionId?: string;
  /** Each side's width; absent: the server's default (15 minutes). */
  windowMs?: number;
}
