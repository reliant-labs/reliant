// Copyright (c) 2025 Reliant Labs

/**
 * FORGE CHANGES, as the console reads them: remote config, gradual rollouts
 * and A/B tests, one record per change per environment (control-plane
 * ChangesService, `controlplane.v1`; design: forge docs/proposals/changes.md).
 *
 * These are the console's own types, not the generated protobuf ones: plain
 * numbers and Dates, enums as words, absent messages as null. changesWire.ts
 * reads the generated messages into them. Field meanings follow changes.proto.
 *
 * The release declares what a change CAN be (changes.yaml: key, type,
 * variants, default — `declaration`); the control plane decides WHO gets it
 * (mode, unit, audience, percentage, split, preview list).
 */

/** Who gets a change right now. `off` is the kill switch: everyone gets the default, previews included. */
export type ChangeMode = "off" | "preview" | "everyone" | "rollout" | "experiment";

/** What stays together: the id a change hashes. */
export type ChangeUnit = "user" | "account" | "visitor";

/** Who is eligible: the app-provided attribute must be one of `in`. Null is everyone. */
export interface ChangeAudience {
  attribute: string;
  in: string[];
}

/** One variant's share of an experiment, in basis points (10000 = 100%). */
export interface ChangeSplitArm {
  variant: string;
  weightBp: number;
}

/** An experiment's one success metric: the share of enrolled units that emit `event` within `windowHours`. */
export interface ChangeMetric {
  event: string;
  prop: string;
  propValue: string;
  /** 0 = the default (168, a week). */
  windowHours: number;
  /** 0 = the readout's default. */
  minSamplePerArm: number;
  controlVariant: string;
}

/** One previewing unit as stored: its id is hashed, never kept. */
export interface ChangePreviewEntry {
  unitHash: string;
  variant: string;
  label: string;
}

/** One previewing unit as the editor names it: a raw id (hashed server-side) or an entry already stored. */
export interface ChangePreviewInput {
  unitId?: string;
  unitHash?: string;
  variant: string;
  label: string;
}

export interface ChangeVariant {
  name: string;
  /** The variant's value as JSON: `true`, `3`, `"blue"`, `{...}`. */
  valueJson: string;
}

/** What the environment's current release declares for the key. */
export interface ChangeDeclaration {
  /** bool, string, number or json. */
  type: string;
  defaultVariant: string;
  variants: ChangeVariant[];
  description: string;
}

/** One randomization epoch: its id is the salt. Launching or changing a running experiment starts a new one. */
export interface ChangeRun {
  id: string;
  /** "experiment" or "rollout". */
  kind: string;
  unit: ChangeUnit;
  audience: ChangeAudience | null;
  split: ChangeSplitArm[];
  variants: ChangeVariant[];
  defaultVariant: string;
  metric: ChangeMetric | null;
  startedAt: Date | null;
  endedAt: Date | null;
  /** "stopped", "ended", "relaunched" or "rolled_out"; empty while open. */
  endedReason: string;
  startedBy: string;
  approvedBy: string;
  /** "not_required", "console_session" or "step_up". */
  approvalMethod: string;
}

export interface Change {
  id: string;
  orgId: string;
  environmentId: string;
  key: string;
  description: string;
  mode: ChangeMode;
  /** Draft: what the next launch freezes. While a run is open, delivery uses its copy. */
  unit: ChangeUnit;
  audience: ChangeAudience | null;
  /** The live percentage of a rollout or experiment, in basis points. */
  percentBp: number;
  split: ChangeSplitArm[];
  everyoneVariant: string;
  rolloutVariant: string;
  preview: ChangePreviewEntry[];
  metric: ChangeMetric | null;
  currentRun: ChangeRun | null;
  /** Every mutation names the revision it read and bumps it by one. */
  revision: number;
  createdAt: Date | null;
  updatedAt: Date | null;
  createdBy: string;
  updatedBy: string;
  /** Null when the environment's current release declares nothing for the key. */
  declaration: ChangeDeclaration | null;
  /** The environment needs a person's approval to launch or ramp up (a protected env, prod). */
  approvalRequired: boolean;
}

/** One entry of a change's audit trail. */
export interface ChangeEvent {
  id: string;
  changeId: string;
  revision: number;
  /** "person", "agent" (a token acting as a person) or "token" (org automation). */
  actorKind: string;
  actorId: string;
  /** create, update_draft, set_preview, launch, ramp, roll_out, stop, end_run. */
  action: string;
  /** `{"<field>": {"from": …, "to": …}}`, parsed; null when it does not parse. */
  diff: Record<string, { from?: unknown; to?: unknown }> | null;
  runId: string;
  approvalMethod: string;
  approvedBy: string;
  createdAt: Date | null;
}

export interface ChangeList {
  changes: Change[];
  nextPageToken: string;
}

export interface ChangeEventPage {
  events: ChangeEvent[];
  nextPageToken: string;
}

/** Where RequestChangeApproval sent the one-time code. */
export interface ChangeApprovalCode {
  /** Masked: j***@example.com. */
  sentTo: string;
  expiresAt: Date | null;
}

// ── Delivery ────────────────────────────────────────────────────────────────

/** Where the platform writes an environment's snapshot: a hosted static site, or the hosted workloads' ConfigMap. */
export type ChangeDeliveryTargetKind = "static-site" | "config-map" | "unknown";

/** GetChangesDeliveryStatus: the snapshot last written to one target. */
export interface ChangeDeliveryTarget {
  kind: ChangeDeliveryTargetKind;
  /** The StaticSite's name, or the ConfigMap as <namespace>/<name>. */
  name: string;
  /** Where the app reads it: the site path, or the file path in the pod. */
  location: string;
  /** The environment snapshot revision last written there. */
  revision: number;
  /** When that content was first written there. */
  deliveredAt: Date | null;
  /** Whether the target's ETag matches the environment's current snapshot. */
  current: boolean;
}

/**
 * GetChangesDeliveryStatus: the revision of the snapshot the environment
 * renders now (the ENVIRONMENT's, not one change's), and what each target
 * last had written to it. An environment with no hosted apps has no targets.
 */
export interface ChangesDeliveryStatus {
  revision: number;
  targets: ChangeDeliveryTarget[];
}

// ── Draft edits ─────────────────────────────────────────────────────────────

/** UpdateChangeDraft: each field changes only when set. */
export interface ChangeDraftEdit {
  description?: string;
  unit?: ChangeUnit;
  /** A new audience; `null` makes it everyone (clear_audience). */
  audience?: ChangeAudience | null;
  split?: ChangeSplitArm[];
  /** A new metric; `null` removes it (clear_metric). */
  metric?: ChangeMetric | null;
}

// ── The readout (results) ───────────────────────────────────────────────────

/**
 * CHANGE READOUT — the results of one experiment run (ChangesService
 * GetChangeReadout, wrapping forge pkg/obsquery ExperimentReadoutQuery and
 * pkg/experimentstats.Analyze). Field names follow the proto.
 *
 * THE SERVER DECIDES; THE CONSOLE RENDERS (the ReleaseComparison model). The
 * verdict, every interval, the sample-ratio check and "inconclusive" are
 * computed server-side and never re-derived here.
 */
export type ChangeReadoutVerdict = "no_data" | "srm_failed" | "inconclusive" | "treatment_better" | "treatment_worse";

/** Why a readout is inconclusive (experimentstats). An unknown reason is shown as inconclusive without one. */
export type ChangeReadoutReason = "below_min_sample" | "window_not_complete" | "interval_spans_zero";

/** Why the readout window ends where it does. */
export type ChangeReadoutEndReason = "now" | "left_experiment" | "run_ended";

/** One version: units counted by assignment (intent-to-treat). */
export interface ChangeReadoutArm {
  variant: string;
  /** The planned share, basis points; 0 for a variant the split does not have. */
  weightBp: number;
  /** Every unit assigned to the arm, window complete or not. */
  assigned: number;
  /** Units whose whole metric window has closed: only these feed the rate and the verdict. */
  windowComplete: number;
  conversions: number;
  /** conversions / windowComplete, and its Wilson interval at 1 − alpha. */
  rate: number;
  ciLow: number;
  ciHigh: number;
  /** assigned − windowComplete: still inside their window, never non-converters. */
  pending: number;
  expectedAssigned: number;
}

/** One treatment against the control. */
export interface ChangeReadoutComparison {
  treatment: string;
  control: string;
  absLift: number;
  /** Null when the control's rate is 0. */
  relLift: number | null;
  /** Newcombe interval of absLift at 1 − alpha (this comparison's, Bonferroni-corrected). */
  diffCiLow: number;
  diffCiHigh: number;
  pValue: number;
  alpha: number;
  significant: boolean;
  verdict: ChangeReadoutVerdict;
  reason: ChangeReadoutReason | "";
}

/** The sample-ratio mismatch check. */
export interface ChangeReadoutSrm {
  /** 0 when skipped. */
  pValue: number;
  chiSquare: number;
  failed: boolean;
  /** Not run: too few units, or fewer than two planned arms. */
  skipped: boolean;
  unplannedVariants: string[];
}

export interface ChangeReadout {
  changeId: string;
  key: string;
  run: ChangeRun | null;
  verdict: ChangeReadoutVerdict;
  reason: ChangeReadoutReason | "";
  /** Set only when verdict is treatment_better. */
  bestVariant: string;
  arms: ChangeReadoutArm[];
  comparisons: ChangeReadoutComparison[];
  srm: ChangeReadoutSrm;
  /** Units assigned more than one version: in no arm. */
  conflicted: number;
  assigned: number;
  pending: number;
  undeclaredVariants: string[];
  readoutStart: Date | null;
  readoutEnd: Date | null;
  readoutEndReason: ChangeReadoutEndReason | "";
  /** The readout window is closed: the numbers no longer move. */
  final: boolean;
  /** The run's frozen metric as the readout applied it (defaults filled in). */
  metric: ChangeMetric | null;
  /** The significance level; confidence is 1 − alpha. */
  alpha: number;
  horizonNote: string;
  /** Events older than their retention are gone: early assignments are not counted. */
  truncatedByRetention: boolean;
}
