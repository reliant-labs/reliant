// The pure domain layer behind the deploy usage & budget surface. No React, no
// RPC — every rule that decides what a customer is TOLD lives here so it can be
// asserted directly.
//
// Ported from control-plane's internal-console `/usage` page, which was built
// against the operator console and is structurally unreachable there: that app
// proxies every request to ADMIN_API_URL, and `adminAPIMounts()` carries only
// the *Admin services. `MountSvcBilling` and `MountDeploy` live in
// `publicAPIMounts()` — the listener THIS app reaches — so the customer's own
// spend controls belong here, beside the compute billing UI in ../.
//
// ── WHERE THE NUMBERS COME FROM ───────────────────────────────────────
//
// Consumption, allowance and ladder rung all come from ONE call:
// BillingService.GetCurrentUserInfraOverage. That RPC exists precisely for
// this surface and folds the four facts together deliberately, because "a
// consumption figure without its allowance is a number with no scale, and a
// ladder rung without the reading behind it cannot be explained to the user
// who is looking at it."
//
// THIS CLIENT DOES NOT CONVERT UNITS, and that is the point. The catalog and
// the meter denominate the same dimensions differently (catalog vCPU-hours vs
// a cpu_milli bucket, GiB-hours vs MiB-hours), and reconciling them wrongly is
// a 1000x or 1024x error in a number a customer is billed against. The server
// already owns that reconciliation in internal/billing/inframeter; the RPC
// reports `consumed` and `included_allowance` in ONE declared `unit`, so the
// comparison here is a plain division and there is no second implementation
// of the conversion to drift from the first.
//
// Attribution is the one thing that RPC does not carry, so it comes from
// DeployService.ListUsage, whose rows have deployment_id / environment_id
// dimensions. Those quantities ARE in raw meter units, so the per-row
// conversion below is confined to attribution display and never touches an
// allowance comparison or a cost.
//
// ── WHY THE WIRE SHAPES ARE DECLARED HERE ─────────────────────────────
//
// The console version imported generated protobuf types. This module declares
// the three shapes it reads as local interfaces instead — the consumer-side
// declaration this repo asks for, and structurally compatible with the
// generated messages, so the adapter that eventually feeds it is a plain
// assignment. It also means the whole domain layer and its tests carry no
// dependency on codegen, which is what makes them assertable in isolation.

/** Raw cpu_milli meter units per vCPU. ATTRIBUTION DISPLAY ONLY. */
export const MILLI_PER_VCPU = 1000;
/** Raw mem_mib meter units per GiB. ATTRIBUTION DISPLAY ONLY. */
export const MIB_PER_GIB = 1024;
/**
 * Mean hours in a month — the ONE place the GiB-hour ↔ GiB-month conversion
 * is declared, matching planlimits.HoursPerMonth. Display only; no comparison
 * or cost figure may route through it.
 */
export const HOURS_PER_MONTH = 730;
/** 1 USD = 1e9 nanos = 100 cents, so a cent is 1e7 nanos. */
const NANOS_PER_CENT = 10_000_000;

/**
 * The meter kinds attribution reads, by their proto tag numbers
 * (controlplane.v1.DeployResourceKind).
 *
 * Only the three that are rendered. Tags 6/7/8 (egress, CDN egress, CDN
 * requests) and the tombstoned 3/4/9 are deliberately absent — see
 * UsageDimensionId.
 */
export const DeployResourceKind = {
  CPU_MILLI: 1,
  MEM_MIB: 2,
  STORAGE_GIB: 10,
} as const;

/** A protobuf Timestamp, as the generated messages carry it. */
export interface WireTimestamp {
  seconds: bigint;
  nanos: number;
}

/** One row of DeployService.ListUsage. */
export interface DeployUsageRow {
  resourceKind: number;
  environmentId: string;
  deploymentId: string;
  quantity: bigint;
  costUsdNanos: bigint;
}

/** One entry of GetCurrentUserInfraOverageResponse.dimensions. */
export interface InfraDimensionUsage {
  dimensionId: string;
  unit: string;
  consumed: number;
  includedAllowance: number;
  action: string;
  suspendable: boolean;
  withinRestoreFloor: boolean;
  overSince?: WireTimestamp;
}

/** BillingService.GetCurrentUserInfraOverage's response. */
export interface InfraOverageResponse {
  dimensions: InfraDimensionUsage[];
  budgetCents?: bigint;
  accruedOverageCents: number;
  budgetCapReached: boolean;
  usageMeasured: boolean;
  periodStart?: WireTimestamp;
  periodEnd?: WireTimestamp;
}

/**
 * The three billable dimensions, and only those three.
 *
 * Egress, CDN egress and CDN requests are deliberately absent: they were
 * removed from the catalog because nothing measures them, and a dimension
 * rendered at zero reads as "you used none" rather than "we do not know".
 * Build minutes are absent for the same reason — the ImageBuild tier is not a
 * product and must not appear on a customer surface.
 */
export type UsageDimensionId = "cpu" | "memory" | "storage";

/**
 * Where a dimension currently sits on the enforcement ladder.
 *
 * These mirror planlimits.OverageAction.String() exactly, which is why the
 * wire carries a STRING rather than an enum: it is already the stable
 * vocabulary the logs and metric labels use, and a parallel enum here would be
 * a second spelling that could drift. `unknown` is the client's own value for
 * a rung the server named that this build does not recognise — rendered as
 * unavailable rather than silently downgraded to "none".
 */
export type LadderRung =
  | "none" // below the notify threshold
  | "notify" // at/past notify, still inside the allowance
  | "block_scale_up" // allowance exhausted: what runs keeps running, nothing new starts
  | "throttle" // continuously over past the grace window: rate-limited, not stopped
  | "suspend" // stopped. Only on an explicit hard cap, and never for storage
  | "unknown";

const KNOWN_RUNGS = new Set<string>([
  "none",
  "notify",
  "block_scale_up",
  "throttle",
  "suspend",
]);

/**
 * Reads the server's rung string.
 *
 * An unrecognised value becomes "unknown" rather than defaulting to "none": a
 * rung this build has never heard of is more likely to be a NEW, more severe
 * one than a benign one, and quietly reporting it as "nothing is happening" is
 * the failure direction that costs the user their service with no warning.
 */
export function parseRung(action: string): LadderRung {
  return KNOWN_RUNGS.has(action) ? (action as LadderRung) : "unknown";
}

export interface DimensionReading {
  id: UsageDimensionId;
  label: string;
  /** What the dimension is metered in, for the "how do you count this" line. */
  meterDescription: string;
  /**
   * Consumed and allowed, in the SAME unit the server declared. No conversion
   * happens on this axis — see the file header for why that matters.
   */
  consumed: number;
  allowance: number;
  /** The server's unit string ("vcpu_hours", "gib_hours"), rendered readably. */
  unitLabel: string;
  /**
   * False when the plan proves no allowance for this dimension. Distinct from
   * an allowance of zero used zero times: with no allowance every unit bills
   * from the first, which is a different sentence.
   */
  hasAllowance: boolean;
  /** 0..N. Over 100 means over the allowance; not clamped. */
  percentUsed: number;
  /** Consumption past the allowance. Zero when inside it. */
  overage: number;
  rung: LadderRung;
  /**
   * Whether the suspend rung may EVER apply. Storage is the standing
   * exception: "suspending" a disk means releasing the PVC, which destroys
   * data to save cents. The server sends this so the UI renders the ladder
   * honestly rather than threatening a suspension that will never happen.
   */
  suspendable: boolean;
  /** When continuous overage began, if it is currently over. */
  overSince?: Date;
  /** Suspension withheld only because the workload was restored too recently. */
  withinRestoreFloor: boolean;
}

export interface AttributionRow {
  deploymentId: string;
  environmentId: string;
  /** Per-dimension consumption in display units. */
  cpuDisplay: number;
  memoryDisplay: number;
  storageDisplay: number;
  /** Accrued cost across every row folded in here, in cents. */
  accruedCents: number;
}

export interface UsageSummary {
  dimensions: DimensionReading[];
  attribution: AttributionRow[];
  /** Sum of cost_usd_nanos over the returned usage rows, in cents. */
  accruedCents: number;
  /** The server's accrued overage this period — the figure the cap is compared against. */
  accruedOverageCents: number;
  /** The server's own verdict on whether the cap has been reached. */
  budgetCapReached: boolean;
  /**
   * The customer's stated deploy ceiling, as a choice rather than a raw number.
   */
  cap: OverageChoice;
  /**
   * FALSE means the server could not measure, and the UI must render
   * "unavailable" rather than a confident zero.
   *
   * This is the field that distinguishes "this org genuinely ran nothing" from
   * "nothing measured it" — two states that otherwise both arrive as 0 on a
   * 200 response. Rendering the second as "0.0 used" is the
   * unknown-as-known defect the server added this flag to prevent, and it
   * fails in the reassuring direction: the user is told they have consumed
   * nothing while real metered usage accrues.
   */
  usageMeasured: boolean;
  /**
   * TRUE for everything on this page, always, and the UI must say so.
   *
   * These are accruals, not invoiced figures: usage is swept from the live
   * cluster and priced against the catalog, Stripe has not seen it, and the
   * period is not closed.
   */
  isEstimate: true;
  periodStart?: Date;
  periodEnd?: Date;
}

/** Presentation metadata the wire deliberately does not carry. */
interface DimensionPresentation {
  id: UsageDimensionId;
  label: string;
  meterDescription: string;
}

/**
 * Maps the server's stable dimension ids to how this product talks about them.
 *
 * ONLY these three are rendered. "vcluster_floor" is a real dimension the RPC
 * may report, but it is not one of the three tiers this product sells, so it
 * is folded out rather than shown as a fourth allowance a customer cannot act
 * on. Egress, CDN and build dimensions cannot appear at all — they were
 * removed from the catalog because nothing measures them.
 */
const PRESENTATION: Record<string, DimensionPresentation> = {
  infra_cpu: {
    id: "cpu",
    label: "CPU",
    meterDescription:
      "Metered from requested CPU × running replicas. Scaling to zero stops this meter.",
  },
  infra_memory: {
    id: "memory",
    label: "Memory",
    meterDescription:
      "Metered from requested memory × running replicas. Scaling to zero stops this meter.",
  },
  infra_storage: {
    id: "storage",
    label: "Storage",
    meterDescription:
      "Metered from the requested volume size while it is bound. Scaling to zero does NOT stop this meter — the disk is still allocated.",
  },
};

/** Renders the server's unit token as something a person reads. */
export function unitLabel(unit: string): string {
  switch (unit) {
    case "vcpu_hours":
      return "vCPU-hours";
    case "gib_hours":
      return "GiB-hours";
    case "minutes":
      return "minutes";
    case "vcluster_hours":
      return "vCluster-hours";
    default:
      // An unrecognised unit is shown verbatim rather than guessed at. The
      // number is still the server's; only the label is unknown.
      return unit;
  }
}

/** protobuf Timestamp → Date. */
export function timestampToDate(ts: WireTimestamp | undefined): Date | undefined {
  if (!ts) return undefined;
  return new Date(Number(ts.seconds) * 1000 + Math.floor(ts.nanos / 1e6));
}

/**
 * Folds usage rows into per-deployment attribution, heaviest spender first.
 *
 * Rows naming no deployment are kept under an empty id rather than dropped: a
 * vCluster-floor row is real money with no deployment to blame, and silently
 * discarding it makes the attribution table disagree with the total above it.
 *
 * The conversions here are the ONLY unit arithmetic in this file, and they are
 * display-only: ListUsage reports raw meter quantities (milli-vCPU-hours,
 * MiB-hours), and nothing downstream compares these against an allowance.
 */
export function buildAttribution(rows: DeployUsageRow[]): AttributionRow[] {
  const byKey = new Map<string, AttributionRow>();
  for (const row of rows) {
    const key = `${row.environmentId}\u0000${row.deploymentId}`;
    let entry = byKey.get(key);
    if (!entry) {
      entry = {
        deploymentId: row.deploymentId,
        environmentId: row.environmentId,
        cpuDisplay: 0,
        memoryDisplay: 0,
        storageDisplay: 0,
        accruedCents: 0,
      };
      byKey.set(key, entry);
    }
    const quantity = Number(row.quantity);
    if (row.resourceKind === DeployResourceKind.CPU_MILLI) {
      entry.cpuDisplay += quantity / MILLI_PER_VCPU;
    } else if (row.resourceKind === DeployResourceKind.MEM_MIB) {
      entry.memoryDisplay += quantity / MIB_PER_GIB;
    } else if (row.resourceKind === DeployResourceKind.STORAGE_GIB) {
      entry.storageDisplay += quantity;
    }
    entry.accruedCents += Number(row.costUsdNanos) / NANOS_PER_CENT;
  }
  return [...byKey.values()].sort((a, b) => b.accruedCents - a.accruedCents);
}

export interface BuildSummaryInput {
  /** The GetCurrentUserInfraOverage response. */
  overage?: InfraOverageResponse;
  /** ListUsage rows, for attribution only. */
  rows: DeployUsageRow[];
}

/** Assembles everything the page renders from the two RPC responses. */
export function buildUsageSummary(input: BuildSummaryInput): UsageSummary {
  const { overage, rows } = input;

  const dimensions: DimensionReading[] = [];
  for (const dimension of overage?.dimensions ?? []) {
    const presentation = PRESENTATION[dimension.dimensionId];
    // A dimension this product does not sell (vcluster_floor) is folded out
    // rather than rendered as an allowance the customer cannot act on.
    if (!presentation) continue;

    const allowance =
      dimension.includedAllowance > 0 ? dimension.includedAllowance : 0;
    const hasAllowance = allowance > 0;
    const consumed = dimension.consumed;
    const percentUsed = hasAllowance
      ? (consumed / allowance) * 100
      : consumed > 0
        ? 100
        : 0;

    dimensions.push({
      id: presentation.id,
      label: presentation.label,
      meterDescription: presentation.meterDescription,
      consumed,
      allowance,
      unitLabel: unitLabel(dimension.unit),
      hasAllowance,
      percentUsed,
      overage: Math.max(0, consumed - allowance),
      // The server's verdict, never re-derived here: it reads the PERSISTED
      // overage streak, which is the only thing that can distinguish a
      // three-day overage from a refresh of the same page.
      rung: parseRung(dimension.action),
      suspendable: dimension.suspendable,
      overSince: timestampToDate(dimension.overSince),
      withinRestoreFloor: dimension.withinRestoreFloor,
    });
  }

  let accruedNanos = 0;
  for (const row of rows) accruedNanos += Number(row.costUsdNanos);

  return {
    dimensions,
    attribution: buildAttribution(rows),
    accruedCents: accruedNanos / NANOS_PER_CENT,
    accruedOverageCents: overage?.accruedOverageCents ?? 0,
    budgetCapReached: overage?.budgetCapReached ?? false,
    cap: overageChoiceFromCap(overage?.budgetCents),
    // Defaults to FALSE deliberately, matching the server's own default: an
    // absent flag means "not proven measured", and withholding is the safe
    // reading.
    usageMeasured: overage?.usageMeasured ?? false,
    isEstimate: true,
    periodStart: timestampToDate(overage?.periodStart),
    periodEnd: timestampToDate(overage?.periodEnd),
  };
}

// ── The budget cap ────────────────────────────────────────────────────
//
// THIS IS THE DEPLOY (INFRA) CAP, NOT THE COMPUTE CAP.
// SetCurrentUserInfraOverage writes subscriptions.infra_budget_cents;
// SetCurrentUserComputeOverage writes a DIFFERENT column that caps daemon
// machines and is driven by ../ComputeOverageControl.tsx. They are
// deliberately separate RPCs — per the proto, "one call writing both would let
// a ceiling set on dev machines arm a suspension of a production site." A
// surface about deployments must write the infra one, or the user caps the
// wrong product.
//
// The infra request REPLACES the stored cap outright; it is not a patch. Its
// proto says so explicitly: "A caller that omits this field is asking for
// uncapped deploy overage, so a UI must send the user's current choice on
// every call, not just when it changes."
//
// A UI that submitted an empty request after the user edited something
// unrelated would therefore SILENTLY REMOVE a cap the user had set, and
// nothing would report it — the request succeeds, the response is a healthy
// subscription, and the next overage bill is unbounded.
//
// The defence is a type, not a convention. OverageChoice makes every state
// name its own cap, so there is no shape the caller can construct that omits
// budget_cents; buildOverageRequest is total over the union and always emits
// the field. "Remember to send the cap" is the rule that gets forgotten at
// the fourth call site — this one cannot compile wrong.
//
// NOTE THERE IS NO `enabled` FLAG. That is not an omission: the infra request
// deliberately has no off switch, because "a deployment that is already
// running accrues usage whether or not anybody opted in. The only thing a
// customer can actually choose is where the ceiling sits." A bool here would
// imply an off position the platform does not have — and it is the one real
// difference from the compute cap beside it, which genuinely can refuse to
// start a machine.

export type OverageChoice =
  /** No ceiling. Explicitly chosen, never inferred from an empty field. */
  | { kind: "uncapped" }
  /** Ceilinged at budgetCents. Must be > 0 to be a cap at all. */
  | { kind: "capped"; budgetCents: number };

export interface OverageRequestFields {
  /**
   * ALWAYS present. Zero is the wire spelling of "no cap" — the enforcement
   * gate tests `> 0` — so an explicit 0 and an omitted field mean the same
   * thing to the server. We send 0 rather than omitting because an omission is
   * indistinguishable from a bug, and this is the field a bug would drop.
   */
  budgetCents: bigint;
}

/** Turns the user's current choice into the full request. Total, by construction. */
export function buildOverageRequest(choice: OverageChoice): OverageRequestFields {
  switch (choice.kind) {
    case "uncapped":
      return { budgetCents: 0n };
    case "capped":
      return { budgetCents: BigInt(Math.round(choice.budgetCents)) };
  }
}

/**
 * Reads the server's stored deploy cap back into a choice.
 *
 * Unset and 0 BOTH resolve to "uncapped", because both are uncapped to the
 * enforcement gate. The UI must never render a 0 as "$0.00": that reads as
 * "I am capped at zero spend, nothing can bill me", which is the exact
 * inverse of what a 0 means.
 */
export function overageChoiceFromCap(budgetCents: bigint | undefined): OverageChoice {
  if (budgetCents === undefined || budgetCents <= 0n) return { kind: "uncapped" };
  return { kind: "capped", budgetCents: Number(budgetCents) };
}

/**
 * Formats cents as USD. Used for caps, accruals and modelled overage alike.
 *
 * Deliberately local rather than billingUtils.formatCentsAsDollars: this
 * module is the domain layer and carries no dependency on the compute billing
 * surface, and these figures are fractional cents from a nanos division where
 * that helper takes whole cents.
 */
export function formatCents(cents: number): string {
  return new Intl.NumberFormat("en-US", {
    style: "currency",
    currency: "USD",
  }).format(cents / 100);
}

/** Formats a metered quantity at a readable precision. */
export function formatQuantity(value: number): string {
  if (value === 0) return "0";
  if (value < 0.01) return "<0.01";
  return new Intl.NumberFormat("en-US", {
    maximumFractionDigits: value < 10 ? 2 : 0,
  }).format(value);
}

/** GiB-hours → GiB-months, for the storage caption only. Never for arithmetic. */
export function gibMonthsFromGibHours(gibHours: number): number {
  return gibHours / HOURS_PER_MONTH;
}
