// Deployment usage & budget — what am I using, what will it cost, where is it
// going, how do I stop it.
//
// Ported from control-plane internal-console's `/usage` page. Three things
// changed in the move and each is deliberate:
//
//  1. It is PROP-DRIVEN, not a page that fetches. Every other cloud settings
//     surface in this directory resolves its data in the parent and hands
//     bands resolved props (see billing.tsx's CreditBand / ComputeBand), which
//     keeps every mutation on one chokepoint in useCloudBillingQueries and
//     makes the states below assertable without a transport. The console
//     version called four hooks inline because it WAS the route.
//  2. It is a SECTION inside the billing surface, not a route of its own. So
//     it owns no page heading — the panel headings and section labels follow
//     the ladder in ../ui/card.tsx.
//  3. It renders against reliant's semantic tokens. Insets are
//     `bg-background` + `border-border/60`; `bg-muted` appears only as
//     interaction state, never as structure.
//
// ── WHAT IS REAL AND WHAT IS NOT ──────────────────────────────────────
//
// Every figure here is an ACCRUAL, not an invoice, and the surface says so
// rather than implying otherwise. Consumption comes from a sweeper that read
// the live cluster and priced it against the catalog — Stripe has not seen it
// and the period is not closed. All of the rules deciding what the customer is
// TOLD live in ./usageModel; this file is composition and the state ladder,
// and it decides nothing about billing.

import { AlertTriangle } from "lucide-react";

import { cn } from "@/lib/utils";

import Button from "@/components/forge-ui/button";
import { Card, CardContent, CardHeader, CardTitle } from "../ui";
import { AllowanceMeter } from "./AllowanceMeter";
import { AttributionTable } from "./AttributionTable";
import { EnforcementPanel } from "./EnforcementPanel";
import { formatCents } from "./usageModel";

import type { UsageSummary } from "./usageModel";

export interface DeployUsageSectionProps {
  /** Everything to render, already folded by buildUsageSummary. */
  summary: UsageSummary;
  /** Still fetching. Renders the skeleton ladder rather than an empty state. */
  isLoading?: boolean;
  /**
   * The load FAILED. Rendered as "we could not read this", never as zero
   * usage — see the error branch.
   */
  error?: { message?: string } | null;
  onRetry?: () => void;
  /** id → name, for the attribution table. */
  deploymentNames?: Map<string, string>;
  environmentNames?: Map<string, string>;
}

function formatPeriod(start?: Date, end?: Date): string {
  if (!start || !end) return "current period";
  const fmt = new Intl.DateTimeFormat("en-US", { month: "short", day: "numeric" });
  return `${fmt.format(start)} – ${fmt.format(end)}`;
}

/** A panel with a heading, matching the Card rhythm the rest of cloud uses. */
function Panel({
  title,
  children,
}: {
  title: string;
  children: React.ReactNode;
}) {
  return (
    <Card>
      <CardHeader>
        <CardTitle>{title}</CardTitle>
      </CardHeader>
      <CardContent>{children}</CardContent>
    </Card>
  );
}

export function DeployUsageSection({
  summary,
  isLoading,
  error,
  onRetry,
  deploymentNames,
  environmentNames,
}: DeployUsageSectionProps) {
  // ── Loading ────────────────────────────────────────────────────────
  if (isLoading) {
    return (
      <section className="flex flex-col gap-4">
        <SectionLabel>Deployments</SectionLabel>
        <p className="text-sm text-muted-foreground">
          Loading your current period…
        </p>
      </section>
    );
  }

  // ── Error ──────────────────────────────────────────────────────────
  // A failed load is NOT rendered as zero usage. A zero would be read as "you
  // have used nothing", which is the unknown-as-known mistake that matters
  // most here because it is the reassuring direction.
  if (error) {
    return (
      <section className="flex flex-col gap-4">
        <SectionLabel>Deployments</SectionLabel>
        <div className="rounded-md border border-destructive/40 bg-destructive/10 p-4">
          <div className="flex items-start gap-3">
            <AlertTriangle
              aria-hidden
              className="mt-0.5 h-4 w-4 shrink-0 text-destructive-ink"
            />
            <div>
              <p className="text-sm font-medium text-foreground">
                Could not load your usage
              </p>
              <p className="mt-1 text-xs leading-relaxed text-muted-foreground">
                This is a problem reading your usage, not a report that you have
                used nothing. Your current consumption is unknown until this
                loads.
                {error.message ? ` (${error.message})` : ""}
              </p>
              {onRetry ? (
                <div className="mt-3">
                  <Button size="md" variant="outline" onClick={onRetry}>
                    Try again
                  </Button>
                </div>
              ) : null}
            </div>
          </div>
        </div>
      </section>
    );
  }

  // Zero included units is normal PAYG, not evidence of a missing subscription.
  const hasDimensions = summary.dimensions.length > 0;

  return (
    <section className="flex flex-col gap-4">
      <div>
        <SectionLabel>Deployments</SectionLabel>
        <p className="mt-0.5 text-sm text-muted-foreground">
          Current billing period (
          {formatPeriod(summary.periodStart, summary.periodEnd)})
        </p>
      </div>

      {/*
        THE ESTIMATE DISCLOSURE. Stated once, prominently, at the top — rather
        than as a footnote under each number — because it qualifies every
        figure here, and a per-number asterisk trains people to stop reading
        it.
      */}
      <p className="text-xs leading-relaxed text-muted-foreground">
        <span className="font-medium text-foreground">
          These are estimates, not an invoice.
        </span>{" "}
        We measure what your deployments are running once a minute and price
        it at app hosting usage rates, so figures trail slightly behind. Your final
        invoice is produced when the period closes and may differ.
      </p>

      <p className="text-sm text-muted-foreground">
        Pay-as-you-go app hosting has no included CPU, memory or storage allowance.
        Machine subscriptions are separate. Authorizing hosting does not bill historical unpaid usage.
      </p>

      {/* 1. Resource usage, independent of machine entitlements. */}
      {hasDimensions ? (
        <Panel title="App hosting resource usage">
          <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
            {summary.dimensions.map((reading) => (
              <AllowanceMeter
                key={reading.id}
                reading={reading}
                usageMeasured={summary.usageMeasured}
              />
            ))}
          </div>
        </Panel>
      ) : null}

      {/* 2. What will this cost me? */}
      <div className="grid gap-4 sm:grid-cols-2">
        <Card>
          <CardContent className="flex flex-col gap-1">
            <StatCaption>Accrued this period</StatCaption>
            <p className="text-2xl font-semibold tabular-nums text-foreground">
              {formatCents(summary.accruedCents)}
            </p>
            <p className="text-xs text-muted-foreground">
              Estimated retail cost of the attribution rows shown below, not a complete monthly charge.
            </p>
          </CardContent>
        </Card>
        <Card>
          <CardContent className="flex flex-col gap-1">
            <StatCaption>Hosting usage charges this period</StatCaption>
            {/*
              The SERVER's accrued-overage figure, not a client re-derivation.
              It is the number the ceiling is compared against, and the proto is
              explicit that the server must own the calculation "or the two will
              disagree."
            */}
            <p
              className={cn(
                "text-2xl font-semibold tabular-nums",
                summary.accruedOverageCents > 0
                  ? "text-destructive-ink"
                  : "text-foreground",
              )}
            >
              {formatCents(summary.accruedOverageCents)}
            </p>
            <p className="text-xs text-muted-foreground">
              {summary.cap.kind === "capped"
                ? `Against your ${formatCents(summary.cap.budgetCents)} monthly ceiling.`
                : summary.accruedOverageCents > 0
                  ? "Metered app hosting usage, independent of machine billing."
                  : "No app hosting usage charges reported for this period."}
            </p>
            {summary.budgetCapReached ? (
              <p className="text-xs font-medium text-destructive-ink">
                You have reached your ceiling — new deployments are refused.
              </p>
            ) : null}
          </CardContent>
        </Card>
      </div>

      {/* 3. Where is it going? */}
      <Panel title="Where it is going">
        <AttributionTable
          rows={summary.attribution}
          deploymentNames={deploymentNames}
          environmentNames={environmentNames}
        />
      </Panel>

      {/* 4. How do I stop it? — plus where the tenant sits on the ladder. */}
      <div className="grid gap-4 lg:grid-cols-2">
        <Panel title="Monthly admission budget">
          <p className="text-sm">Your budget is recorded with the app hosting billing agreement above. Changing it is not supported here.</p>
          <p className="mt-2 text-xs text-muted-foreground">Admission and suspension follow measured usage. Enforcement can lag and in-flight work may keep accruing charges; this is not a guaranteed maximum bill. Storage is not deleted.</p>
        </Panel>
        <Panel title="Enforcement">
          <EnforcementPanel dimensions={summary.dimensions} />
        </Panel>
      </div>
    </section>
  );
}

/** Section label rung of the heading ladder — see ../ui/card.tsx. */
function SectionLabel({ children }: { children: React.ReactNode }) {
  return (
    <h3 className="text-xs font-semibold uppercase tracking-wide text-muted-foreground">
      {children}
    </h3>
  );
}

/** Stat caption — one rung below the ladder, lighter than a section label. */
function StatCaption({ children }: { children: React.ReactNode }) {
  return (
    <p className="text-xs font-medium uppercase tracking-wide text-muted-foreground">
      {children}
    </p>
  );
}
