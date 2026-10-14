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

/** The bounds GetDoraMetrics accepts for N (`failure_window_hours`); 24 is the server's default. */
export const FAILURE_WINDOW_HOURS = { min: 1, max: 168, default: 24 } as const;

/** ListReleases' own bounds: the newest `limit` (max 2000) over at most 366 days. */
export const RELEASES_LIMIT = { default: 500, max: 2000 } as const;
export const DELIVERY_MAX_RANGE_DAYS = 366;
