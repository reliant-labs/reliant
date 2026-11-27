// Copyright (c) 2026 Reliant Labs

/**
 * shop's Experiments (Forge Changes: remote config, gradual rollouts and A/B
 * tests), told in shopStory's terms, as WIRE messages: handlers/changes.ts
 * serves them as ChangesService's answers, and the pages' real client maps
 * them (services/controlPlane/changes/changesWire.ts).
 *
 *   the mitigation  checkout.payment_retry in prod: v1.8.3 declares it, on by
 *                   default (the retry the incident is about). Minutes ago
 *                   maya gave everyone `off` (one charge attempt with the whole
 *                   3s deadline, as before v1.8.3) while v1.8.4 is in dev.
 *   the fix         the same key in dev, where v1.8.4 declares it off by
 *                   default: maya and jon preview the retry `on` with the new
 *                   deadline and budget before anyone else gets it.
 *   the rollout     confirmation.order_status in prod, v1.8.2's order status
 *                   on the confirmation page (priya): 5%, then 10%, now 25% of
 *                   users.
 *   the A/B test    express-checkout in prod (jon): the storefront's one-page
 *                   checkout against the current one, the flag the Analytics
 *                   page's flag-exposure panel shows (audienceActivity.ts
 *                   DEMO_FLAG). Every user is in it, split 50/50, measured on
 *                   `purchase` within 24 hours: `on` converts better.
 *   the finished    banner.free_shipping in prod (priya): a free-shipping
 *                   banner against none, per visitor, for six days. It ended
 *                   two days ago with no clear difference, and is off.
 *
 * The readouts' arithmetic is the server's (forge pkg/experimentstats: Wilson
 * intervals, Newcombe's interval for the difference, the pooled z-test, a
 * chi-square sample-ratio check), computed here from the counts, so a rate,
 * its interval and the verdict printed beside them agree.
 *
 * Times are measured from demoNow() (the page load), like every other demo page.
 */

import { create, type MessageInitShape } from "@bufbuild/protobuf";
import { timestampFromMs } from "@bufbuild/protobuf/wkt";

import {
  ChangeDeliveryTargetKind,
  ChangeEventSchema,
  ChangeMode,
  ChangeReadoutVerdict,
  ChangeSchema,
  ChangeUnit,
  GetChangeReadoutResponseSchema,
  GetChangesDeliveryStatusResponseSchema,
  type Change,
  type ChangeEvent,
  type GetChangeReadoutResponse,
  type GetChangesDeliveryStatusResponse,
} from "@/gen/controlplane/services/changes/v1/changes_pb";

import { DAY, DEMO_ORG, demoNow, demoPerson, HOUR, MINUTE, type DemoPerson } from "../shopStory";

type ChangeInit = MessageInitShape<typeof ChangeSchema>;
/** One entry of a change's audit trail as the story writes it; the pack mints its id. */
interface EventInit {
  revision: bigint;
  actorKind: string;
  actorId: string;
  action: string;
  diffJson: string;
  createdAt: ReturnType<typeof timestampFromMs>;
  runId?: string;
  approvalMethod?: string;
  approvedBy?: string;
}

const email = (who: DemoPerson) => demoPerson(who)!.email;
const at = (now: number, ago: number) => timestampFromMs(now - ago);

// ── The declarations: what each release's changes.yaml says ─────────────────

const bool = (name: string, on: boolean) => ({ name, valueJson: on ? "true" : "false" });

const PAYMENT_RETRY_V183 = {
  type: "bool",
  defaultVariant: "on",
  variants: [bool("on", true), bool("off", false)],
  description: "Retry a payment charge once when the provider times out.",
};
/** v1.8.4 (dev) ships the retry off by default: it is turned on, with its own deadline and budget, by this change. */
const PAYMENT_RETRY_V184 = { ...PAYMENT_RETRY_V183, defaultVariant: "off", description: "Retry a payment charge once, inside its own 2s deadline and retry budget." };
const ORDER_STATUS = { type: "bool", defaultVariant: "off", variants: [bool("off", false), bool("on", true)], description: "Show the order's live status on the confirmation page." };
const EXPRESS_CHECKOUT = { type: "bool", defaultVariant: "control", variants: [bool("control", false), bool("on", true)], description: "One-page express checkout on the storefront." };
const FREE_SHIPPING = {
  type: "string",
  defaultVariant: "none",
  variants: [
    { name: "none", valueJson: '""' },
    { name: "banner", valueJson: '"Free shipping on orders over $50"' },
  ],
  description: "The storefront's free-shipping banner.",
};

// ── The readouts' arithmetic (forge pkg/experimentstats) ────────────────────

const Z = 1.959963984540054;
/** The error function, to ~1e-7 (Abramowitz and Stegun 7.1.26). */
function erf(x: number): number {
  const t = 1 / (1 + 0.3275911 * Math.abs(x));
  const y = 1 - ((((1.061405429 * t - 1.453152027) * t + 1.421413741) * t - 0.284496736) * t + 0.254829592) * t * Math.exp(-x * x);
  return x >= 0 ? y : -y;
}
function wilson(x: number, n: number): [number, number, number] {
  if (n === 0) return [0, 0, 1];
  const p = x / n;
  const d = 1 + (Z * Z) / n;
  const center = (p + (Z * Z) / (2 * n)) / d;
  const half = (Z * Math.sqrt((p * (1 - p)) / n + (Z * Z) / (4 * n * n))) / d;
  return [p, center - half, center + half];
}

interface ArmCounts {
  variant: string;
  weightBp: number;
  assigned: number;
  windowComplete: number;
  conversions: number;
}

/** A two-arm readout (control first) from its counts: arms, the comparison, the sample-ratio check and the verdict. */
function readoutOf(control: ArmCounts, treatment: ArmCounts) {
  const assigned = control.assigned + treatment.assigned;
  const arms = [control, treatment].map((arm) => {
    const [rate, ciLow, ciHigh] = wilson(arm.conversions, arm.windowComplete);
    return { ...arm, rate, ciLow, ciHigh, pending: arm.assigned - arm.windowComplete, expectedAssigned: (assigned * arm.weightBp) / 10000 };
  });
  const wire = arms.map((arm) => ({ ...arm, assigned: BigInt(arm.assigned), windowComplete: BigInt(arm.windowComplete), conversions: BigInt(arm.conversions), pending: BigInt(arm.pending) }));
  const [c, t] = arms;
  const absLift = t.rate - c.rate;
  const diffCiLow = absLift - Math.sqrt((t.rate - t.ciLow) ** 2 + (c.ciHigh - c.rate) ** 2);
  const diffCiHigh = absLift + Math.sqrt((t.ciHigh - t.rate) ** 2 + (c.rate - c.ciLow) ** 2);
  const pooled = (c.conversions + t.conversions) / (c.windowComplete + t.windowComplete);
  const se = Math.sqrt(pooled * (1 - pooled) * (1 / c.windowComplete + 1 / t.windowComplete));
  const pValue = 1 - erf(Math.abs(absLift / se) / Math.SQRT2);
  const alpha = 0.05;
  const significant = pValue < alpha && (diffCiLow > 0 || diffCiHigh < 0);
  const verdict = !significant ? ChangeReadoutVerdict.INCONCLUSIVE : absLift > 0 ? ChangeReadoutVerdict.TREATMENT_BETTER : ChangeReadoutVerdict.TREATMENT_WORSE;
  const chiSquare = arms.reduce((sum, arm) => sum + (arm.assigned - arm.expectedAssigned) ** 2 / arm.expectedAssigned, 0);
  return {
    arms: wire,
    comparisons: [
      { treatment: t.variant, control: c.variant, absLift, relLift: absLift / c.rate, diffCiLow, diffCiHigh, pValue, alpha, significant, verdict, reason: significant ? "" : "interval_spans_zero" },
    ],
    srm: { pValue: 1 - erf(Math.sqrt(chiSquare / 2)), chiSquare, failed: false, skipped: false },
    verdict,
    reason: significant ? "" : "interval_spans_zero",
    bestVariant: verdict === ChangeReadoutVerdict.TREATMENT_BETTER ? t.variant : "",
    assigned: BigInt(assigned),
    pending: BigInt(arms.reduce((sum, arm) => sum + arm.pending, 0)),
    alpha,
  };
}

const HORIZON_NOTE = "Decide once, at the sample size you planned, not at the first look that is significant.";

// ── The changes ─────────────────────────────────────────────────────────────

interface StoryChange {
  change: ChangeInit;
  /** Its audit trail, oldest first (revision 1 is the create). */
  events: EventInit[];
  /** Its experiment readout, if it ever ran one. */
  readout?: () => MessageInitShape<typeof GetChangeReadoutResponseSchema>;
}

function storyChanges(now: number): StoryChange[] {
  const event = (revision: number, who: DemoPerson, action: string, ago: number, diff: Record<string, { from: unknown; to: unknown }> = {}, extra: Partial<EventInit> = {}): EventInit => ({
    revision: BigInt(revision),
    actorKind: "person",
    actorId: email(who),
    action,
    diffJson: Object.keys(diff).length ? JSON.stringify(diff) : "",
    createdAt: at(now, ago),
    ...extra,
  });
  // prod is protected: a launch, ramp up or roll-out there needs a person, and the console session is one.
  const approved = (who: DemoPerson) => ({ approvalMethod: "console_session", approvedBy: email(who) });

  const expressRun = {
    id: "run-express-checkout-1",
    changeId: "chg-express-checkout-prod",
    kind: "experiment",
    unit: ChangeUnit.USER,
    split: [
      { variant: "control", weightBp: 5000 },
      { variant: "on", weightBp: 5000 },
    ],
    variants: EXPRESS_CHECKOUT.variants,
    defaultVariant: "control",
    metric: { event: "purchase", windowHours: 24, minSamplePerArm: BigInt(4000), controlVariant: "control" },
    startedAt: at(now, 8 * DAY),
    startedBy: email("jon"),
    approvedBy: email("jon"),
    approvalMethod: "console_session",
  };
  const bannerRun = {
    id: "run-free-shipping-1",
    changeId: "chg-free-shipping-prod",
    kind: "experiment",
    unit: ChangeUnit.VISITOR,
    split: [
      { variant: "none", weightBp: 5000 },
      { variant: "banner", weightBp: 5000 },
    ],
    variants: FREE_SHIPPING.variants,
    defaultVariant: "none",
    metric: { event: "purchase", windowHours: 24, minSamplePerArm: BigInt(8000), controlVariant: "none" },
    startedAt: at(now, 8 * DAY + 4 * HOUR),
    endedAt: at(now, 2 * DAY),
    endedReason: "ended",
    startedBy: email("priya"),
    approvedBy: email("priya"),
    approvalMethod: "console_session",
  };
  const orderStatusRun = {
    id: "run-order-status-1",
    changeId: "chg-order-status-prod",
    kind: "rollout",
    unit: ChangeUnit.USER,
    variants: ORDER_STATUS.variants,
    defaultVariant: "off",
    startedAt: at(now, 4.5 * DAY),
    startedBy: email("priya"),
    approvedBy: email("priya"),
    approvalMethod: "console_session",
  };

  return [
    {
      change: {
        id: "chg-payment-retry-prod",
        environmentId: "env-prod",
        key: "checkout.payment_retry",
        description: "Off while checkout's incident is open: one charge attempt, as before v1.8.3.",
        mode: ChangeMode.EVERYONE,
        unit: ChangeUnit.USER,
        everyoneVariant: "off",
        revision: BigInt(2),
        createdAt: at(now, 7 * MINUTE),
        updatedAt: at(now, 6 * MINUTE),
        createdBy: email("maya"),
        updatedBy: email("maya"),
        declaration: PAYMENT_RETRY_V183,
        approvalRequired: true,
      },
      events: [
        event(1, "maya", "create", 7 * MINUTE),
        event(2, "maya", "roll_out", 6 * MINUTE, { mode: { from: "off", to: "everyone" }, everyone_variant: { from: "", to: "off" } }, approved("maya")),
      ],
    },
    {
      change: {
        id: "chg-payment-retry-dev",
        environmentId: "env-dev",
        key: "checkout.payment_retry",
        description: "v1.8.4's retry, with its own deadline and budget: maya and jon try it first.",
        mode: ChangeMode.PREVIEW,
        unit: ChangeUnit.USER,
        preview: [
          { unitHash: "3fa91c07", variant: "on", label: email("maya") },
          { unitHash: "b20e6d14", variant: "on", label: email("jon") },
        ],
        revision: BigInt(2),
        createdAt: at(now, 18 * MINUTE),
        updatedAt: at(now, 15 * MINUTE),
        createdBy: email("maya"),
        updatedBy: email("maya"),
        declaration: PAYMENT_RETRY_V184,
      },
      events: [event(1, "maya", "create", 18 * MINUTE), event(2, "maya", "set_preview", 15 * MINUTE, { mode: { from: "off", to: "preview" }, preview: { from: 0, to: 2 } })],
    },
    {
      change: {
        id: "chg-order-status-prod",
        environmentId: "env-prod",
        key: "confirmation.order_status",
        description: "v1.8.2's live order status on the confirmation page.",
        mode: ChangeMode.ROLLOUT,
        unit: ChangeUnit.USER,
        percentBp: 2500,
        rolloutVariant: "on",
        currentRun: orderStatusRun,
        revision: BigInt(4),
        createdAt: at(now, 4.5 * DAY + 20 * MINUTE),
        updatedAt: at(now, DAY + 3 * HOUR),
        createdBy: email("priya"),
        updatedBy: email("priya"),
        declaration: ORDER_STATUS,
        approvalRequired: true,
      },
      events: [
        event(1, "priya", "create", 4.5 * DAY + 20 * MINUTE),
        event(2, "priya", "roll_out", 4.5 * DAY, { mode: { from: "off", to: "rollout" }, rollout_variant: { from: "", to: "on" }, percent: { from: "0%", to: "5%" } }, { ...approved("priya"), runId: orderStatusRun.id }),
        event(3, "priya", "ramp", 3 * DAY, { percent: { from: "5%", to: "10%" } }, { ...approved("priya"), runId: orderStatusRun.id }),
        event(4, "priya", "ramp", DAY + 3 * HOUR, { percent: { from: "10%", to: "25%" } }, { ...approved("priya"), runId: orderStatusRun.id }),
      ],
    },
    {
      change: {
        id: "chg-express-checkout-prod",
        environmentId: "env-prod",
        key: "express-checkout",
        description: "One-page checkout against the current three steps.",
        mode: ChangeMode.EXPERIMENT,
        unit: ChangeUnit.USER,
        percentBp: 10000,
        split: expressRun.split,
        metric: expressRun.metric,
        preview: [{ unitHash: "e47c2a90", variant: "on", label: email("priya") }],
        currentRun: expressRun,
        revision: BigInt(6),
        createdAt: at(now, 8.5 * DAY),
        updatedAt: at(now, 6 * DAY),
        createdBy: email("jon"),
        updatedBy: email("jon"),
        declaration: EXPRESS_CHECKOUT,
        approvalRequired: true,
      },
      events: [
        event(1, "jon", "create", 8.5 * DAY),
        event(2, "jon", "update_draft", 8.4 * DAY, { split: { from: "none", to: "control 50% / on 50%" }, metric: { from: "none", to: "purchase within 24h" } }),
        event(3, "priya", "set_preview", 8.3 * DAY, { preview: { from: 0, to: 1 } }),
        event(4, "jon", "launch", 8 * DAY, { mode: { from: "off", to: "experiment" }, percent: { from: "0%", to: "10%" } }, { ...approved("jon"), runId: expressRun.id }),
        event(5, "jon", "ramp", 7 * DAY, { percent: { from: "10%", to: "50%" } }, { ...approved("jon"), runId: expressRun.id }),
        event(6, "jon", "ramp", 6 * DAY, { percent: { from: "50%", to: "100%" } }, { ...approved("jon"), runId: expressRun.id }),
      ],
      readout: () => ({
        run: expressRun,
        ...readoutOf(
          { variant: "control", weightBp: 5000, assigned: 6214, windowComplete: 5630, conversions: 1874 },
          { variant: "on", weightBp: 5000, assigned: 6151, windowComplete: 5571, conversions: 2068 }
        ),
        readoutStart: expressRun.startedAt,
        readoutEnd: at(now, 0),
        readoutEndReason: "now",
        final: false,
        metric: expressRun.metric,
      }),
    },
    {
      change: {
        id: "chg-free-shipping-prod",
        environmentId: "env-prod",
        key: "banner.free_shipping",
        description: "A free-shipping banner against none. Ended: no clear difference.",
        mode: ChangeMode.OFF,
        unit: ChangeUnit.VISITOR,
        split: bannerRun.split,
        metric: bannerRun.metric,
        revision: BigInt(4),
        createdAt: at(now, 8.5 * DAY + 2 * HOUR),
        updatedAt: at(now, 2 * DAY),
        createdBy: email("priya"),
        updatedBy: email("priya"),
        declaration: FREE_SHIPPING,
        approvalRequired: true,
      },
      events: [
        event(1, "priya", "create", 8.5 * DAY + 2 * HOUR),
        event(2, "priya", "update_draft", 8.5 * DAY, { unit: { from: "user", to: "visitor" }, split: { from: "none", to: "none 50% / banner 50%" }, metric: { from: "none", to: "purchase within 24h" } }),
        event(3, "priya", "launch", 8 * DAY + 4 * HOUR, { mode: { from: "off", to: "experiment" }, percent: { from: "0%", to: "100%" } }, { ...approved("priya"), runId: bannerRun.id }),
        event(4, "priya", "end_run", 2 * DAY, { mode: { from: "experiment", to: "off" } }, { runId: bannerRun.id }),
      ],
      readout: () => ({
        run: bannerRun,
        ...readoutOf(
          { variant: "none", weightBp: 5000, assigned: 9804, windowComplete: 9804, conversions: 1127 },
          { variant: "banner", weightBp: 5000, assigned: 9771, windowComplete: 9771, conversions: 1148 }
        ),
        readoutStart: bannerRun.startedAt,
        readoutEnd: bannerRun.endedAt,
        readoutEndReason: "run_ended",
        final: true,
        metric: bannerRun.metric,
      }),
    },
  ];
}

// ── Delivery: where each environment's snapshot was written ─────────────────

/** Each environment's snapshot revision (it grows with every change mutation and promotion) and when it was last written. */
const SNAPSHOTS: Record<string, { revision: number; writtenAgo: number }> = {
  "env-dev": { revision: 12, writtenAgo: 15 * MINUTE },
  "env-staging": { revision: 7, writtenAgo: 3 * HOUR },
  "env-prod": { revision: 41, writtenAgo: 6 * MINUTE },
};

const ENV_NAMES: Record<string, string> = { "env-dev": "dev", "env-staging": "staging", "env-prod": "prod" };

function deliveryStatus(envId: string, now: number): GetChangesDeliveryStatusResponse {
  const snapshot = SNAPSHOTS[envId];
  if (!snapshot) return create(GetChangesDeliveryStatusResponseSchema);
  // Shaped like the server's hex SHA-256 of the snapshot; nothing reads its value.
  const etag = `${ENV_ORDER.indexOf(envId)}${snapshot.revision.toString(16)}`.padStart(8, "0").repeat(8);
  const target = (kind: ChangeDeliveryTargetKind, name: string, location: string, lagMs: number) => ({
    kind,
    name,
    location,
    revision: BigInt(snapshot.revision),
    etag,
    deliveredAt: at(now, snapshot.writtenAgo - lagMs),
    current: true,
  });
  return create(GetChangesDeliveryStatusResponseSchema, {
    revision: BigInt(snapshot.revision),
    etag,
    targets: [
      target(ChangeDeliveryTargetKind.STATIC_SITE, "shop-web", "/_forge/changes.json", 2_000),
      target(ChangeDeliveryTargetKind.CONFIG_MAP, `shop-${ENV_NAMES[envId]}/reliant-forge-changes`, "/etc/forge/changes/changes.json", 9_000),
    ],
  });
}

// ── The pack's answers ──────────────────────────────────────────────────────

export interface ShopChangesAnswers {
  /** Every change, ordered by environment then key, narrowed to one environment when one is named. */
  listChanges: (environmentId: string | undefined) => Change[];
  /** One change, or undefined for an id the story has none of. */
  getChange: (changeId: string) => Change | undefined;
  /** A change's audit trail, newest first. */
  listChangeEvents: (changeId: string) => ChangeEvent[];
  /** The readout of a change's latest experiment run; undefined when it never ran one. */
  getChangeReadout: (changeId: string) => GetChangeReadoutResponse | undefined;
  getChangesDeliveryStatus: (environmentId: string) => GetChangesDeliveryStatusResponse;
}

/** ENV_ORDER: dev, staging, prod, as the console lists environments. */
const ENV_ORDER = ["env-dev", "env-staging", "env-prod"];

export function shopChangesAnswers(now: number = demoNow(), orgId: string = DEMO_ORG): ShopChangesAnswers {
  const story = storyChanges(now).map((entry) => {
    const change = create(ChangeSchema, { ...entry.change, orgId });
    const events = entry.events
      .map((e, i) => create(ChangeEventSchema, { ...e, id: `${change.id}-ev${i + 1}`, changeId: change.id }))
      .reverse();
    return { change, events, readout: entry.readout };
  });
  const byId = new Map(story.map((entry) => [entry.change.id, entry]));
  const ordered = [...story].sort((a, b) => ENV_ORDER.indexOf(a.change.environmentId) - ENV_ORDER.indexOf(b.change.environmentId) || a.change.key.localeCompare(b.change.key));
  return {
    listChanges: (environmentId) => ordered.filter((entry) => !environmentId || entry.change.environmentId === environmentId).map((entry) => entry.change),
    getChange: (changeId) => byId.get(changeId)?.change,
    listChangeEvents: (changeId) => byId.get(changeId)?.events ?? [],
    getChangeReadout: (changeId) => {
      const entry = byId.get(changeId);
      if (!entry?.readout) return undefined;
      return create(GetChangeReadoutResponseSchema, { changeId, key: entry.change.key, horizonNote: HORIZON_NOTE, ...entry.readout() });
    },
    getChangesDeliveryStatus: (environmentId) => deliveryStatus(environmentId, now),
  };
}
