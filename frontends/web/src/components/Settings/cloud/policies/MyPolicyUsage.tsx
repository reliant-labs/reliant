// Copyright (c) 2025 Reliant Labs

/**
 * "Your usage against these limits" on the Spend limits card: the caller's
 * own spend this period against the member cap that binds them, and the
 * org's spend against the org cap (GetMyPolicyUsage, org-policies §6, D15).
 * Every member sees it: a cap no one can see is a cap no one can comply with.
 *
 * The numbers are the gateway's, counted per UTC calendar period as the cap
 * counts them. While the caps are only trialled (audit), going over is said
 * to be counted, not refused. A control plane that predates the rpc answers
 * Unimplemented, shown as "not available yet", never as an error.
 */

import Button from "@/components/forge-ui/button";
import ProgressBar from "@/components/forge-ui/progress_bar";
import { SpendAppliesTo, SpendPeriod, type SpendCap } from "@/gen/controlplane/controlplane/v1/org_policy_pb";
import { isUsageUnavailable, policyErrorMessage, type SpendUsage } from "@/services/controlPlane/orgPolicy";

import { formatUsdNanos } from "./policyDocument";
import { useMyPolicyUsage } from "./usePolicies";

const ADJECTIVE: Record<SpendPeriod, string> = {
  [SpendPeriod.UNSPECIFIED]: "",
  [SpendPeriod.DAY]: "daily",
  [SpendPeriod.MONTH]: "monthly",
};

/** Percent of the cap used; a cap of zero is full from the start. */
export function usedPercent(spent: bigint, cap: bigint): number {
  if (cap <= 0n) return 100;
  return Number((spent * 1000n) / cap) / 10;
}

/** "Resets Dec 1" for a monthly cap, "Resets at midnight UTC" for a daily one. */
export function resetWords(cap: SpendCap, periodEnd: Date | null): string {
  if (cap.period === SpendPeriod.DAY) return "Resets at midnight UTC.";
  if (!periodEnd) return "";
  return `Resets ${periodEnd.toLocaleDateString("en-US", { month: "short", day: "numeric", timeZone: "UTC" })}.`;
}

function variantFor(pct: number): "default" | "warning" | "danger" {
  if (pct >= 100) return "danger";
  if (pct >= 80) return "warning";
  return "default";
}

/** One cap's line and bar. `subject` is "You" or the org's name. */
function CapUsage({ testId, subject, own, usage, cap, note }: { testId: string; subject: string; own: string; usage: SpendUsage; cap: SpendCap; note?: string }) {
  const pct = usedPercent(usage.spentUsdNanos, cap.usdNanos);
  const verb = subject === "You" ? "You've" : `${subject} has`;
  const sentence = `${verb} used ${formatUsdNanos(usage.spentUsdNanos)} of ${own} ${formatUsdNanos(cap.usdNanos)} ${ADJECTIVE[cap.period]} limit${note ?? ""}.`;
  return (
    <div className="space-y-1" data-testid={testId}>
      <p className="text-sm text-foreground">{sentence}</p>
      <ProgressBar value={Math.min(pct, 100)} ariaLabel={sentence} variant={variantFor(pct)} size="sm" />
      <p className="text-xs text-muted-foreground">{resetWords(cap, usage.periodEnd)}</p>
    </div>
  );
}

export function MyPolicyUsage({ orgId, orgName }: { orgId: string; orgName: string }) {
  const usage = useMyPolicyUsage(orgId);

  if (usage.isPending) {
    return (
      <p className="text-sm text-muted-foreground" role="status" data-testid="policy-my-usage">
        Loading your usage…
      </p>
    );
  }
  if (usage.isError) {
    if (isUsageUnavailable(usage.error)) {
      return (
        <p className="text-sm text-muted-foreground" data-testid="policy-my-usage">
          Your usage against these limits: not available yet.
        </p>
      );
    }
    return (
      <div className="flex items-center gap-3" role="alert" data-testid="policy-my-usage">
        <p className="flex-1 text-sm text-muted-foreground">{policyErrorMessage(usage.error, "Could not load your usage.")}</p>
        <Button variant="outline" size="sm" onClick={() => void usage.refetch()}>
          Try again
        </Button>
      </div>
    );
  }

  const { member, org, enforced, appliesTo } = usage.data;
  const anyCap = !!member?.cap || !!org?.cap;
  const over =
    (!!member?.cap && member.spentUsdNanos >= member.cap.usdNanos) || (!!org?.cap && org.spentUsdNanos >= org.cap.usdNanos);

  return (
    <section className="space-y-3 rounded-md border border-border/60 bg-background p-3" aria-label="Your usage" data-testid="policy-my-usage">
      {member?.cap ? (
        <CapUsage testId="policy-usage-member" subject="You" own="your" usage={member} cap={member.cap} />
      ) : (
        member && (
          <p className="text-sm text-foreground" data-testid="policy-usage-member">
            You've used {formatUsdNanos(member.spentUsdNanos)} this month.{" "}
            {member.unlimited ? "You have no personal limit." : "There's no limit per member."}
          </p>
        )
      )}
      {org?.cap && (
        <CapUsage
          testId="policy-usage-org"
          subject={orgName}
          own="its"
          usage={org}
          cap={org.cap}
          note={appliesTo === SpendAppliesTo.MEMBERS_AND_APPS ? ", counting its hosted apps" : undefined}
        />
      )}
      {anyCap && !enforced && (
        <p className="text-xs text-muted-foreground" data-testid="policy-usage-audit">
          {over ? "Over the limit: in audit mode that is counted, and nothing is refused." : "Audit mode: going over a limit is counted, not refused."}
        </p>
      )}
    </section>
  );
}
