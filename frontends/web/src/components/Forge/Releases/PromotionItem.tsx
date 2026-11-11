// Copyright (c) 2025 Reliant Labs

/**
 * One promotion as a ledger row: the release (linked to its page), its marks
 * (rollback, scoped, …), who and when, the checks, a health verdict and a
 * digest, with any further lines under it. The workload History tab's
 * Releases panel and the environment page's Promotions card both draw their
 * rows with it, so a promotion reads the same wherever it appears; each
 * caller decides only where its health and digest come from.
 */

import type { ReactNode } from "react";

import Link from "@/components/forge-ui/link";
import Status from "@/components/forge-ui/status";

import type { PromotionHealth } from "./envPromotionsModel";
import { Ago, ChecksMark, INK_LINK } from "./releaseParts";
import { shortVersion, type ChecksSummary } from "./releaseModel";

export interface PromotionItemProps {
  testId: string;
  version: string;
  href: string;
  rollback: boolean;
  /** Tags after the version: rollback, scoped, redeploy. */
  marks?: ReactNode;
  by: string;
  at?: string;
  checks: ChecksSummary | null;
  health: ReactNode;
  digest: ReactNode;
  /** Lines under the row: what changed, the note. */
  children?: ReactNode;
}

export function PromotionItem({ testId, version, href, rollback, marks, by, at, checks, health, digest, children }: PromotionItemProps) {
  return (
    <li className="space-y-1 py-2.5 text-xs first:pt-0 last:pb-0" data-testid={testId} data-version={version} {...(rollback ? { "data-rollback": "" } : {})}>
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <Link href={href} className={`${INK_LINK} text-sm`} title={version}>
          {shortVersion(version)}
        </Link>
        {marks}
        <span className="text-ink-muted">
          {by ? `${by} · ` : ""}
          <Ago at={at} />
        </span>
        <span className="inline-flex items-center gap-1 text-ink-muted">
          checks <ChecksMark checks={checks} />
        </span>
        {health}
        <span className="inline-flex items-center gap-1 text-ink-muted">{digest}</span>
      </div>
      {children}
    </li>
  );
}

const HEALTH_WORDS: Record<PromotionHealth, { tone: "success" | "danger" | "warning" | "neutral"; label: string; title: string }> = {
  healthy: { tone: "success", label: "healthy", title: "The release-health check recorded on this promotion passed." },
  regressed: { tone: "danger", label: "regressed", title: "The release-health check recorded on this promotion found a regression." },
  inconclusive: { tone: "warning", label: "not proven", title: "The release-health check recorded on this promotion reached no verdict: not a pass." },
  unknown: {
    tone: "neutral",
    label: "unknown",
    title: "No release-health check was recorded on this promotion (`forge obs check <env> --gate-json` with `forge gate record`).",
  },
};

/** The health verdict a promotion recorded. Unknown reads as unknown, never as a pass. */
export function PromotionHealthMark({ health }: { health: { verdict: PromotionHealth; summary: string } }) {
  const words = HEALTH_WORDS[health.verdict];
  return (
    <span title={health.summary ? `${words.title}\n${health.summary}` : words.title} data-health={health.verdict}>
      <Status tone={words.tone} label={`health: ${words.label}`} />
    </span>
  );
}
