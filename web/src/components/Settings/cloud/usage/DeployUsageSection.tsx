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

import { Button, Card, CardContent, CardHeader, CardTitle } from "../ui";
import { AllowanceMeter } from "./AllowanceMeter";
import { AttributionTable } from "./AttributionTable";
import { BudgetCapControl } from "./BudgetCapControl";
import { EnforcementPanel } from "./EnforcementPanel";
import { formatCents } from "./usageModel";

import type { OverageRequestFields, UsageSummary } from "./usageModel";

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
  /** Receives the FULL infra overage request — budgetCents, always. */
  onSaveCap: (fields: OverageRequestFields) => void;
  isSavingCap?: boolean;
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
  onSaveCap,
  isSavingCap,
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
              className="mt-0.5 h-4 w-4 shrink-0 text-destructive"
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
                  <Button size="sm" variant="outline" onClick={onRetry}>
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

  // A plan that declares no infrastructure dimensions at all is the
  // no-subscription state: the server reports allowances from the active
  // compute subscription and from nowhere else.
  const hasPlan = summary.dimensions.length > 0;

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
        Usage is measured after the fact by a sweeper that reads your running
        services, then priced against your plan. Your final invoice is produced
        when the period closes and may differ.
      </p>

      {/*
        NO SUBSCRIPTION. Explained, not rendered as an empty dashboard: there
        is no free tier, so this state is normal for a new account rather than
        an error, and the consequence (every unit bills from the first, one
        deployment permitted) is what the user actually needs to know.
      */}
      {!hasPlan ? (
        <div className="rounded-md border border-warning/40 bg-warning/10 p-4">
          <p className="text-sm font-medium text-foreground">
            No compute plan on this account
          </p>
          <div className="mt-2 flex flex-col gap-2 text-xs leading-relaxed text-muted-foreground">
            <p>
              You have access to deployments, but no compute plan — so there is
              no included allowance to draw down. There is no free tier.
            </p>
            <p>
              Until a plan is active you can run{" "}
              <span className="font-medium text-foreground">one</span>{" "}
              deployment, and any usage bills from the first unit.
            </p>
          </div>
        </div>
      ) : null}

      {/* 1. What am I using, against what am I allowed? */}
      {hasPlan ? (
        <Panel title="Consumption against allowance">
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
              Estimated cost of everything metered so far, including usage inside
              your allowance.
            </p>
          </CardContent>
        </Card>
        <Card>
          <CardContent className="flex flex-col gap-1">
            <StatCaption>Overage this period</StatCaption>
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
                  ? "text-destructive"
                  : "text-foreground",
              )}
            >
              {formatCents(summary.accruedOverageCents)}
            </p>
            <p className="text-xs text-muted-foreground">
              {summary.cap.kind === "capped"
                ? `Against your ${formatCents(summary.cap.budgetCents)} monthly ceiling.`
                : summary.accruedOverageCents > 0
                  ? "Consumption past your included allowance. You have no ceiling set."
                  : "You are inside your included allowance on every dimension."}
            </p>
            {summary.budgetCapReached ? (
              <p className="text-xs font-medium text-destructive">
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
        <Panel title="Spending cap">
          <BudgetCapControl
            value={summary.cap}
            onSubmit={onSaveCap}
            isSubmitting={isSavingCap}
            disabledReason={
              hasPlan
                ? undefined
                : "A spending ceiling limits overage against a plan's included allowance. With no plan on this account there is no allowance to exceed."
            }
          />
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
