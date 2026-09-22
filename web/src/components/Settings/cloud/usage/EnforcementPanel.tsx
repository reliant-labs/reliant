// Where the tenant currently sits on the infrastructure enforcement ladder,
// and what each tier of product actually does when a limit is hit.
//
// ── THE RUNG IS THE SERVER'S, NOT OURS ────────────────────────────────
//
// Every rung shown here comes from InfraDimensionUsage.action, which the
// server evaluates against the PERSISTED overage streak
// (controlplane.infra_overage_state). That persistence is why "throttled" can
// be stated honestly at all: throttling depends on how long a dimension has
// been continuously over, which no instantaneous percentage can tell you, and
// which survives a page refresh only because the server stores it.
//
// So this component computes no thresholds. Re-deriving the ladder client-side
// would produce a second implementation that disagrees with the one actually
// enforcing — and the disagreement would surface as a user being refused a
// scale-up the dashboard said was fine.
//
// ── WHAT IT REFUSES TO IMPLY ──────────────────────────────────────────
//
// The per-tier behaviour is spelled out because it genuinely differs, and a
// single generic "your service may be affected" would be a lie to two of the
// three tiers: only SimpleBackend is ever scaled to zero. ManagedDatabase and
// StaticSite keep running and are alerted.
//
// Storage never shows a suspend rung, whatever the policy says, because
// `suspendable` is false for it on the wire — "suspending" a disk means
// releasing the PVC, which destroys data rather than stopping a cost.

import { cn } from "@/lib/utils";

import type { DimensionReading, LadderRung } from "./usageModel";

export interface EnforcementPanelProps {
  dimensions: DimensionReading[];
}

// Severity order, worst last. `unknown` is deliberately NOT in this list: an
// unrecognised rung cannot be ranked, so it is surfaced separately rather than
// silently sorted below a rung it might outrank.
const RUNG_SEVERITY: LadderRung[] = [
  "none",
  "notify",
  "block_scale_up",
  "throttle",
  "suspend",
];

const RUNG_COPY: Record<LadderRung, { title: string; body: string }> = {
  none: {
    title: "Inside your allowance",
    body: "No restrictions are in effect.",
  },
  notify: {
    title: "Approaching your allowance",
    body: "Nothing is restricted yet. Scale-ups are blocked once an allowance is exhausted.",
  },
  block_scale_up: {
    title: "Scale-ups are blocked",
    body: "Everything already running is unaffected, but new deployments, extra replicas and new clusters are refused — if a change you made did not take effect, this is why.",
  },
  throttle: {
    title: "Throttled",
    body: "You have been over your allowance long enough that workloads are now rate-limited. They are still running and still serving, just more slowly.",
  },
  suspend: {
    title: "Suspended",
    body: "Affected workloads have been stopped. Storage is never suspended, so your data is intact and restarting restores service.",
  },
  unknown: {
    title: "Enforcement state unavailable",
    body: "The platform reported a state this app does not recognise. Treat your services as potentially restricted and check back shortly.",
  },
};

const RUNG_TONE: Record<LadderRung, "ok" | "warn" | "bad"> = {
  none: "ok",
  notify: "warn",
  block_scale_up: "bad",
  throttle: "bad",
  suspend: "bad",
  unknown: "warn",
};

// Tinted callouts, not structural insets: the border+wash carries the tone,
// which is why these use the semantic accent tokens rather than bg-background.
const TONE_STYLES = {
  ok: "border-success/40 bg-success/10 text-foreground",
  warn: "border-warning/40 bg-warning/10 text-foreground",
  bad: "border-destructive/40 bg-destructive/10 text-foreground",
};

function worstRung(dimensions: DimensionReading[]): LadderRung {
  // An unrecognised rung wins outright: it cannot be ranked, and assuming it
  // is milder than something we do recognise is the unsafe direction.
  if (dimensions.some((d) => d.rung === "unknown")) return "unknown";
  let worst: LadderRung = "none";
  for (const dimension of dimensions) {
    if (RUNG_SEVERITY.indexOf(dimension.rung) > RUNG_SEVERITY.indexOf(worst)) {
      worst = dimension.rung;
    }
  }
  return worst;
}

const TIER_BEHAVIOUR = [
  {
    tier: "Backend services",
    effect: "Scaled to 0 replicas",
    detail:
      "Requests return 503. Fully reversible — your configuration is untouched and it restarts once you are back inside the allowance.",
    tone: "danger" as const,
  },
  {
    tier: "Databases",
    effect: "Keeps running",
    detail:
      "A managed database is never stopped over a limit, and its storage is never released. You are alerted instead.",
    tone: "success" as const,
  },
  {
    tier: "Static sites",
    effect: "Keeps serving",
    detail: "Static sites continue to serve. You are alerted instead.",
    tone: "success" as const,
  },
];

const TIER_TONE = {
  danger: "text-destructive",
  success: "text-success",
};

function formatOverSince(date: Date): string {
  return new Intl.DateTimeFormat("en-US", {
    month: "short",
    day: "numeric",
    hour: "numeric",
    minute: "2-digit",
  }).format(date);
}

export function EnforcementPanel({ dimensions }: EnforcementPanelProps) {
  const current = worstRung(dimensions);
  const copy = RUNG_COPY[current];
  const affected = dimensions.filter((d) => d.rung !== "none");

  return (
    <div className="flex flex-col gap-3">
      <div className={cn("rounded-md border p-3", TONE_STYLES[RUNG_TONE[current]])}>
        <p className="text-sm font-medium">{copy.title}</p>
        <p className="mt-1 text-xs leading-relaxed">{copy.body}</p>
        {affected.length > 0 && current !== "unknown" ? (
          <p className="mt-1 text-xs leading-relaxed">
            Affected: {affected.map((d) => d.label.toLowerCase()).join(", ")}.
          </p>
        ) : null}
      </div>

      {/* Per-dimension detail, but only where there is something to say. */}
      {affected.length > 0 ? (
        <ul className="flex flex-col gap-2">
          {affected.map((dimension) => (
            <li
              key={dimension.id}
              className="rounded-md border border-border/60 bg-background p-3"
            >
              <div className="flex items-baseline justify-between gap-3">
                <span className="text-sm font-medium text-foreground">
                  {dimension.label}
                </span>
                <span className="shrink-0 text-xs text-muted-foreground">
                  {RUNG_COPY[dimension.rung].title}
                </span>
              </div>
              {dimension.overSince ? (
                <p className="mt-1 text-xs text-muted-foreground">
                  Over since {formatOverSince(dimension.overSince)}.
                </p>
              ) : null}
              {dimension.withinRestoreFloor ? (
                <p className="mt-1 text-xs text-warning">
                  Running again for now because it was restarted recently — this
                  reprieve is temporary.
                </p>
              ) : null}
              {!dimension.suspendable ? (
                <p className="mt-1 text-xs text-muted-foreground">
                  This is never suspended: releasing storage would destroy your
                  data rather than stop a cost.
                </p>
              ) : null}
            </li>
          ))}
        </ul>
      ) : null}

      <h4 className="mt-2 text-xs font-semibold uppercase tracking-wide text-muted-foreground">
        What happens to each kind of service
      </h4>
      <dl className="flex flex-col gap-2">
        {TIER_BEHAVIOUR.map((tier) => (
          <div
            key={tier.tier}
            className="rounded-md border border-border/60 bg-background p-3"
          >
            <div className="flex items-baseline justify-between gap-3">
              <dt className="text-sm font-medium text-foreground">{tier.tier}</dt>
              <dd className={cn("shrink-0 text-xs font-medium", TIER_TONE[tier.tone])}>
                {tier.effect}
              </dd>
            </div>
            <dd className="mt-1 text-xs leading-relaxed text-muted-foreground">
              {tier.detail}
            </dd>
          </div>
        ))}
      </dl>
    </div>
  );
}
