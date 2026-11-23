// Copyright (c) 2026 Reliant Labs

/**
 * The Overview's Vulnerabilities card: per environment, the known
 * vulnerabilities in the release it runs (critical, high, medium and low
 * counts, how many are on CISA's KEV list, how many have a fix) and when it
 * was last scanned, with one link to Security › Vulnerabilities.
 *
 * It reads GetVulnerabilitySummary, the same read the Vulnerabilities page
 * starts from (hooks/vulnerability-queries.ts), and keeps its rule: UNKNOWN IS
 * NEVER CLEAN (vulnModel.ts). Before any scan it says "Not scanned yet"; a
 * control plane without the service, or a caller without vuln:read, says so
 * (VulnStates.tsx's words); "No known vulnerabilities" is said only when every
 * environment it covers is fully scanned against a current database and
 * nothing was found. An environment short of that says what it is short of.
 */

import type { ReactNode } from "react";
import { ArrowRight } from "lucide-react";

import Link from "@/components/forge-ui/link";
import Tag from "@/components/forge-ui/tag";
import { useVulnerabilitySummary } from "@/hooks/vulnerability-queries";
import { vulnErrorKind } from "@/services/controlPlane/vulnerabilities/errors";
import type { EnvVulnSummary } from "@/services/controlPlane/vulnerabilities/types";
import { cn } from "@/lib/utils";

import { formatAgo, formatWhen } from "../Observability/format";
import { ObsLoadingRows } from "../Observability/ObsStates";
import { INK_BUTTON, Panel } from "../Releases/releaseParts";
import { vulnErrorCopy } from "./VulnStates";
import { envVouchedClean, overviewVulns, SCAN_STATE_LABEL, SEVERITY_LABEL, totalFindings } from "./vulnModel";

export interface VulnerabilitiesCardProps {
  /** The forge project. */
  project: string;
  /** The top bar's environment name, or null for every environment. */
  envName: string | null;
  /** Security › Vulnerabilities, in the same scope. */
  href: string;
}

const SHOWN = ["critical", "high", "medium", "low"] as const;
const ABBR: Record<(typeof SHOWN)[number], string> = { critical: "C", high: "H", medium: "M", low: "L" };

export function VulnerabilitiesCard({ project, envName, href }: VulnerabilitiesCardProps) {
  const summary = useVulnerabilitySummary(project);
  const link = (
    <Link href={href} className="no-color inline-flex h-7 items-center gap-1 rounded-md px-2 text-xs text-ink-muted hover:bg-ink/5 hover:text-ink" data-testid="overview-vulns-link">
      View vulnerabilities
      <ArrowRight className="h-3 w-3" aria-hidden="true" />
    </Link>
  );
  const frame = (children: ReactNode, state: string) => (
    <Panel title="Vulnerabilities" actions={link} flush testId="overview-vulns">
      <div className="px-4 pb-3 pt-1" data-state={state}>
        {children}
      </div>
    </Panel>
  );

  // Still loading, or waiting for the org scope (the read is disabled until it resolves): nothing to say yet.
  if (summary.isPending) return frame(<ObsLoadingRows rows={2} testId="overview-vulns-loading" />, "loading");
  if (summary.error || !summary.data) {
    const kind = vulnErrorKind(summary.error);
    const copy = vulnErrorCopy(kind, "this project's vulnerabilities");
    return frame(
      <div data-testid={`overview-vulns-error-${kind}`}>
        <p className="text-ui text-ink">{copy.title}</p>
        <p className="mt-1 text-meta text-ink-muted">{copy.body}</p>
        {kind === "unavailable" && (
          <button type="button" className={cn(INK_BUTTON, "mt-1 text-meta")} onClick={() => void summary.refetch()} disabled={summary.isFetching} data-testid="overview-vulns-retry">
            {summary.isFetching ? "Retrying…" : "Try again"}
          </button>
        )}
      </div>,
      "error"
    );
  }

  const view = overviewVulns(summary.data, envName);
  const now = Date.now();
  if (view.kind === "not-scanned") {
    return frame(
      <div data-testid="overview-vulns-not-scanned">
        <p className="text-ui text-ink">Not scanned yet</p>
        <p className="mt-1 text-meta text-ink-muted">
          No release running {envName ? `in ${envName}` : "here"} has a scanned SBOM yet, so its vulnerabilities are unknown, not absent. forge records the SBOMs when it cuts a
          release.
        </p>
      </div>,
      "not-scanned"
    );
  }
  if (view.kind === "clean") {
    const builtAt = summary.data.database?.builtAtMs ?? null;
    return frame(
      <div data-testid="overview-vulns-none-known">
        <p className="text-ui text-ink">No known vulnerabilities</p>
        <p className="mt-1 text-meta text-ink-muted">
          Every artifact {envName ? `in ${envName}` : "in every environment"} has an SBOM, and its newest scan found nothing
          {builtAt ? `, against a database built ${formatAgo(builtAt, now)}` : ""}.
        </p>
      </div>,
      "clean"
    );
  }
  const databaseStale = summary.data.database?.stale ?? false;
  return frame(
    <ul className="divide-y divide-border-subtle" data-testid="overview-vulns-envs">
      {view.envs.map((env) => (
        <EnvLine key={env.environmentId} env={env} databaseStale={databaseStale} now={now} />
      ))}
    </ul>,
    "findings"
  );
}

function EnvLine({ env, databaseStale, now }: { env: EnvVulnSummary; databaseStale: boolean; now: number }) {
  const total = totalFindings(env.counts);
  const other = env.counts.negligible + env.counts.unknown;
  const scanned = env.coverage.scanned > 0;
  // What falls short of a current, complete scan, said beside whatever was found.
  const shortOf = env.scanState === "scanned" ? (databaseStale ? SCAN_STATE_LABEL.stale : null) : SCAN_STATE_LABEL[env.scanState];
  return (
    <li className="py-2 first:pt-1 last:pb-0" data-testid={`overview-vulns-env-${env.environmentName}`} data-scan-state={env.scanState}>
      <div className="flex items-baseline justify-between gap-2">
        <span className="min-w-0 truncate text-ui">
          <span className="font-medium text-ink">{env.environmentName}</span>
          <span className="ml-2 font-mono text-meta text-ink-muted">{env.releaseVersion}</span>
        </span>
        <span className="shrink-0 text-meta text-ink-muted" title={env.lastScannedMs ? formatWhen(env.lastScannedMs, now) : undefined} data-testid="overview-vulns-scanned">
          {env.lastScannedMs ? `scanned ${formatAgo(env.lastScannedMs, now)}` : "never scanned"}
        </span>
      </div>
      {!scanned ? (
        <p className="mt-0.5 text-meta text-ink-muted">{shortOf ?? SCAN_STATE_LABEL["no-sbom"]}: its vulnerabilities are unknown</p>
      ) : total === 0 ? (
        <p className="mt-0.5 text-meta text-ink-muted">
          {envVouchedClean(env, databaseStale) ? "No known vulnerabilities" : `Nothing found in what was scanned${shortOf ? ` · ${shortOf.toLowerCase()}` : ""}`}
        </p>
      ) : (
        <div className="mt-1 flex flex-wrap items-center gap-x-3 gap-y-1 text-meta">
          <span className="inline-flex gap-2 tabular-nums" data-testid="overview-vulns-counts">
            {SHOWN.map((sev) => (
              <span key={sev} title={SEVERITY_LABEL[sev]} className={env.counts[sev] > 0 && (sev === "critical" || sev === "high") ? "text-danger-ink" : "text-ink-muted"}>
                <abbr title={SEVERITY_LABEL[sev]} className="no-underline">
                  {ABBR[sev]}
                </abbr>{" "}
                <span className={env.counts[sev] > 0 ? "font-medium" : undefined}>{env.counts[sev]}</span>
              </span>
            ))}
            {other > 0 && <span className="text-ink-muted">+{other} other</span>}
          </span>
          {env.kev > 0 && (
            <span data-testid={`overview-vulns-kev-${env.environmentName}`}>
              <Tag tone="danger" title="On CISA's Known Exploited Vulnerabilities list">
                {env.kev} KEV
              </Tag>
            </span>
          )}
          {env.fixable > 0 && <span className="text-ink-muted">{env.fixable} fixable</span>}
          {shortOf && <span className="text-ink-muted">{shortOf.toLowerCase()}</span>}
        </div>
      )}
    </li>
  );
}
