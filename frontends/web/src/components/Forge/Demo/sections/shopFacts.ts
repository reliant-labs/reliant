// Copyright (c) 2026 Reliant Labs

/**
 * The sample shop's story in plain words, for the demo's own pages (Home,
 * How releases work) and the explanations it adds to the console's issues.
 * Every figure comes from the SAME story the console's pages read
 * (demoData/shopStory.ts), so the Home's "checkouts fail" is the open issue
 * on Errors and the failed check on Releases. Nothing here invents a number.
 */

import type { Issue } from "@/services/controlPlane/observability/issuesTypes";

import { DEMO_ENV_IDS, DEMO_INCIDENT, RUNNING, type DemoEnvKey } from "../demoData/shopStory";

/** What each of the shop's issues means, for someone who never read a stack trace: the demo's `explainIssue`. */
const SHOP_ISSUE_EXPLANATIONS: Record<string, string> = {
  [DEMO_INCIDENT.issueId]: "Since the latest release, a slow payment is retried, and the retry runs past checkout's 3-second time limit.",
  "iss-inventory-lock": "Two orders tried to reserve the same product at the same moment.",
  "iss-confirmation-status": "The page expected an order status that older orders did not have.",
  "iss-dev-deprecated": "A warning only developers see; it does not affect customers.",
};

export const explainShopIssue = (issue: Pick<Issue, "id">): string | undefined => SHOP_ISSUE_EXPLANATIONS[issue.id];

/** The incident's check, in plain numbers: errors and slow checkouts before → after. */
export const INCIDENT_CHECK = { errorsBefore: "0%", errorsAfter: `${(DEMO_INCIDENT.failShare * 100).toFixed(1)}%`, slowBefore: "0.1s", slowAfter: "3.0s" };

export interface ReleaseStop {
  env: DemoEnvKey;
  version: string;
  ageMs: number;
  by: string;
  check: "passed" | "failed" | "none";
  checkSummary: string;
}

/** What each environment runs now, in promotion order, with the health check its promotion recorded. */
export function runningNow(): ReleaseStop[] {
  return DEMO_ENV_IDS.map((env) => {
    const promotion = RUNNING[env];
    const health = promotion.health ?? (env === "env-dev" ? "none" : { status: "passed" as const, summary: "" });
    return {
      env,
      version: promotion.version,
      ageMs: promotion.age,
      by: promotion.by,
      check: health === "none" ? "none" : health.status,
      checkSummary: health === "none" ? "" : health.summary,
    };
  });
}

/** "40 minutes ago", "3 hours ago", "6 days ago". */
export function ago(ms: number): string {
  const minutes = Math.round(ms / 60_000);
  if (minutes < 1) return "just now";
  if (minutes < 60) return `${minutes} minute${minutes === 1 ? "" : "s"} ago`;
  const hours = Math.round(minutes / 60);
  if (hours < 24) return `${hours} hour${hours === 1 ? "" : "s"} ago`;
  const days = Math.round(hours / 24);
  return `${days} day${days === 1 ? "" : "s"} ago`;
}
