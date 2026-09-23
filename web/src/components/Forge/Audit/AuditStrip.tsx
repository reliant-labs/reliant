// Copyright (c) 2025 Reliant Labs

/**
 * The project-audit STATUS STRIP — compact by design.
 *
 * This is per-project static health, not environment topology, and it must not
 * compete with the env checks below it for attention. So it is one collapsed
 * line by default: a verdict badge, the project's name and kind, and counts. The
 * ~18 categories (and the ~58KB of `details` that comes with them on a project
 * the size of control-plane) render only when a reader asks, and even then only
 * their one-line summaries. The details bag is never dumped.
 *
 * That collapsed-context role is why this file does NOT get the full Card
 * treatment the env-status tables get. It is a single bordered strip that opens
 * into a table; a card here would make the least important surface on the screen
 * the most prominent one.
 *
 * SEVERITY IS GATED ON `error` ONLY. This is the rule that decides whether the
 * strip is useful or ignored. control-plane's real audit today is overall_status
 * "warn" — config_deps and migration_safety both warn on a project that is
 * healthy and shipping. A strip that goes red on that is red permanently, and a
 * permanently-red indicator teaches people to stop looking, which is strictly
 * worse than having no indicator. Warnings get their own visible, calm treatment;
 * only an error gets the destructive one. See services/forge/audit.ts.
 *
 * `file_sizes` is advisory: forge holds its status at "ok" even while listing
 * oversized files, and stamps `details.advisory: true`. It renders as the OK it
 * is, with its advisory nature labelled. Branching on its findings instead of its
 * status would invent a failure forge explicitly declined to report.
 *
 * Categories are iterated from the document. Nothing is hardcoded to a known
 * list: forge's contract is additive, and some categories only exist for certain
 * project shapes.
 *
 * Read-only: it reports, it never offers to fix.
 */

import { useState } from "react";
import { AlertTriangle, ChevronDown, ChevronRight, CircleCheck, HelpCircle, OctagonAlert } from "lucide-react";
import type { LucideIcon } from "lucide-react";

import { cn } from "@/lib/utils";
import { Badge } from "@/components/ui";
import type { BadgeProps } from "@/components/ui/Badge";
import type { ForgeOutcome } from "@/services/forge/topology";
import {
  auditCategories,
  auditStatusLabel,
  severityTally,
  verdictLabel,
  verdictOf,
  verdictSentence,
  type AuditSeverity,
  type AuditVerdict,
  type ForgeAuditReport,
} from "@/services/forge/audit";

/**
 * SEVERITY_STYLES — four levels, and the split between `notice` and `problem` is
 * the whole point.
 *
 * `notice` (warn) is SOLID and visible in the warning hue: it was assessed, and
 * it has something to say. `problem` (error) is the only destructive treatment in
 * the file. `unreadable` borrows the dashed, ringed, unfilled treatment the env
 * status panel gives an undetermined check, because it means the same thing — a
 * status this build could not read is a gap in the report, not a pass and not a
 * failure.
 *
 * These layer OVER a shared `Badge` variant (below). The variant carries the hue;
 * these carry fill, border style and ring, which are the axes that survive a
 * greyscale screenshot and which Badge has no variant for.
 */
const SEVERITY_STYLES: Record<AuditSeverity, string> = {
  clean: "border-solid border-success/40 bg-success/15 text-success",
  notice: "border-solid border-warning/40 bg-warning/15 text-warning",
  problem: "border-solid border-destructive/40 bg-destructive/15 text-destructive",
  unreadable:
    "border-dashed border-muted-foreground/60 bg-transparent text-muted-foreground ring-1 ring-inset ring-muted-foreground/30",
};

/** The shared Badge variant per severity. `unreadable` is the only unhued one. */
const SEVERITY_BADGE_VARIANT: Record<AuditSeverity, NonNullable<BadgeProps["variant"]>> = {
  clean: "success",
  notice: "warning",
  problem: "destructive",
  unreadable: "outline",
};

const SEVERITY_ICONS: Record<AuditSeverity, LucideIcon> = {
  clean: CircleCheck,
  notice: AlertTriangle,
  problem: OctagonAlert,
  unreadable: HelpCircle,
};

/** The verdict's own treatment, mapped through the same four severity levels. */
const VERDICT_SEVERITY: Record<AuditVerdict, AuditSeverity> = {
  problem: "problem",
  notice: "notice",
  clean: "clean",
  unreadable: "unreadable",
  // No categories is not health and not a fault: nothing was assessed.
  "no-categories": "unreadable",
};

export interface AuditStripProps {
  outcome: ForgeOutcome<ForgeAuditReport> | undefined;
  isLoading: boolean;
  /** A transport/daemon failure — genuinely an error, unlike every outcome below. */
  error?: Error | null;
  projectName?: string;
}

/** Shared column-header treatment, matching the env-status tables. */
const TH = "px-3 py-2 text-left text-xs font-medium text-muted-foreground";

export function AuditStrip({ outcome, isLoading, error, projectName }: AuditStripProps) {
  const [expanded, setExpanded] = useState(false);

  if (isLoading && !outcome) {
    return (
      <div
        data-testid="forge-audit-loading"
        className="rounded-lg border border-dashed border-border px-3 py-2 text-sm text-muted-foreground"
      >
        Auditing {projectName ?? "this project"}…
      </div>
    );
  }

  if (error && !outcome) {
    return (
      <div
        data-testid="forge-audit-error"
        className="rounded-lg border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm text-destructive"
      >
        Could not reach your daemon to audit this project: {error.message}
      </div>
    );
  }

  if (!outcome) return null;

  /**
   * The non-report outcomes get ONE-LINE renderings here rather than the shared
   * full-panel ForgeStates components. Those are correct for a screen; this is a
   * strip, and a twelve-line empty state inside it would make the compact
   * surface the loudest thing on the page — the opposite of the requirement.
   *
   * None of them is styled as an error, because none of them is one.
   */
  if (outcome.kind !== "report") {
    return <AuditNotice outcome={outcome} projectName={projectName} />;
  }

  const report = outcome.report;
  const rows = auditCategories(report);
  const verdict = verdictOf(report);
  const severity = VERDICT_SEVERITY[verdict];
  const tally = severityTally(report);
  const VerdictIcon = SEVERITY_ICONS[severity];

  return (
    <section
      data-testid="forge-audit-strip"
      data-verdict={verdict}
      className="rounded-lg border border-border"
    >
      <button
        type="button"
        onClick={() => setExpanded((previous) => !previous)}
        aria-expanded={expanded}
        className="flex w-full items-center gap-x-3 rounded-lg px-3 py-2 text-left hover:bg-accent/50"
      >
        {expanded ? (
          <ChevronDown className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden="true" />
        ) : (
          <ChevronRight className="h-4 w-4 shrink-0 text-muted-foreground" aria-hidden="true" />
        )}

        {/* This ONE chip is a <span>, not the shared <Badge>, and the reason is
            structural rather than stylistic: Badge renders a <div>, and a
            <button>'s content model is phrasing content, so a Badge here would
            be invalid markup. It carries the same severity classes as the Badge
            in the category table below, so the two still read identically.
            Worth fixing properly by giving Badge an `as` prop in
            components/ui/Badge.tsx — out of scope for this pass. */}
        <span
          data-testid="forge-audit-verdict"
          data-severity={severity}
          className={cn(
            // The verdict word is prose ("Needs attention"), not an identifier.
            "inline-flex shrink-0 items-center gap-1.5 rounded-md border px-2 py-0.5 text-xs font-medium",
            SEVERITY_STYLES[severity]
          )}
        >
          <VerdictIcon className="h-3.5 w-3.5 shrink-0" aria-hidden="true" />
          <span>{verdictLabel(verdict)}</span>
        </span>

        <span className="min-w-0 flex-1 truncate text-sm text-muted-foreground">
          {/* The project name is an identifier; the kind and the count are not. */}
          <span className="font-mono text-foreground">
            {report.project_name || projectName || "Project"}
          </span>
          {report.project_kind ? ` · ${report.project_kind}` : ""}
          {` · ${rows.length} categor${rows.length === 1 ? "y" : "ies"}`}
        </span>

        {/* Counts, not a single number: "2 warnings, 0 errors" is a different
            statement from "2 findings", and the difference is exactly what keeps
            a warn-only project from reading as broken. Not monospaced — these
            are quantities, and tabular-nums is what stops them jittering. */}
        <span className="shrink-0 text-xs tabular-nums text-muted-foreground">
          {tally.problem} error{tally.problem === 1 ? "" : "s"} · {tally.notice} warn
          {tally.notice === 1 ? "" : "s"}
          {tally.unreadable > 0 ? ` · ${tally.unreadable} unreadable` : ""}
        </span>
      </button>

      {/* The verdict sentence sits outside the collapsed row only when it needs
          to: a warn-only project is where a reader is most likely to over-read
          the badge, so it says in words that warnings are not errors. */}
      {!expanded && (verdict === "notice" || verdict === "unreadable") && (
        <p data-testid="forge-audit-verdict-note" className="px-3 pb-2 text-xs text-muted-foreground">
          {verdictSentence(verdict)}
        </p>
      )}

      {expanded && (
        <div className="border-t border-border">
          <div className="space-y-1 px-3 py-2">
            <p className="text-xs text-muted-foreground">{verdictSentence(verdict)}</p>

            {report.overall_status && (
              <p className="text-xs text-muted-foreground">
                forge&apos;s own roll-up:{" "}
                <span className="font-mono text-foreground">{report.overall_status}</span>
                {report.binary_version ? (
                  <>
                    {" · forge "}
                    <span className="font-mono">{report.binary_version}</span>
                  </>
                ) : null}
              </p>
            )}
          </div>

          {rows.length === 0 ? (
            <p data-testid="forge-audit-no-categories" className="px-3 pb-2 text-sm text-muted-foreground">
              Forge&apos;s audit returned no categories, so nothing has been assessed.
            </p>
          ) : (
            /* One row per category, as a real table: the status, the category
               key, and its summary each get a column, so eighteen categories
               scan as eighteen lines instead of eighteen ragged wrapped
               paragraphs. The inset well is bg-background + border-border/60 —
               never bg-muted, which inverts direction between light and dark. */
            <div className="border-t border-border/60 bg-background">
              <table className="w-full border-collapse text-left">
                <thead>
                  <tr className="border-b border-border/60">
                    <th scope="col" className={TH}>
                      Status
                    </th>
                    <th scope="col" className={TH}>
                      Category
                    </th>
                    <th scope="col" className={TH}>
                      Summary
                    </th>
                  </tr>
                </thead>
                <tbody data-testid="forge-audit-categories">
                  {rows.map((row) => {
                    const Icon = SEVERITY_ICONS[row.severity];
                    return (
                      <tr
                        key={row.key}
                        data-testid={`forge-audit-category-${row.key}`}
                        data-severity={row.severity}
                        data-audit-status={row.status ?? ""}
                        data-advisory={row.advisory ? "true" : undefined}
                        className="border-b border-border/60 last:border-b-0"
                      >
                        <td className="whitespace-nowrap px-3 py-2 align-top">
                          <Badge
                            data-severity-badge={row.severity}
                            variant={SEVERITY_BADGE_VARIANT[row.severity]}
                            size="sm"
                            className={cn(
                              "gap-1 rounded-md font-sans hover:scale-100",
                              SEVERITY_STYLES[row.severity]
                            )}
                          >
                            <Icon className="h-3 w-3 shrink-0" aria-hidden="true" />
                            <span>{auditStatusLabel(row.status)}</span>
                          </Badge>
                        </td>

                        {/* The category key is forge's identifier, verbatim. */}
                        <td className="whitespace-nowrap px-3 py-2 align-top font-mono text-sm text-foreground">
                          {row.key}
                        </td>

                        <td className="px-3 py-2 align-top text-sm text-muted-foreground">
                          {row.summary || <span aria-hidden="true">—</span>}
                          {/* Advisory is labelled where it appears, so a reader
                              who sees findings listed under an OK category knows
                              forge meant them as a hint, not a defect. */}
                          {row.advisory && (
                            <span className="ml-2 whitespace-nowrap text-xs text-muted-foreground">
                              advisory · non-gating
                            </span>
                          )}
                        </td>
                      </tr>
                    );
                  })}
                </tbody>
              </table>
            </div>
          )}
        </div>
      )}
    </section>
  );
}

/**
 * The one-line renderings of the non-report outcomes.
 *
 * `unreachable` keeps the dashed unknown treatment rather than a destructive one,
 * consistently with everywhere else on this surface — though note the audit is
 * pure static analysis over local files, so the api-server passes
 * readsCluster=false for it and this arm should be rare.
 */
function AuditNotice({
  outcome,
  projectName,
}: {
  outcome: Exclude<ForgeOutcome<ForgeAuditReport>, { kind: "report" }>;
  projectName?: string;
}) {
  const shell = (testId: string, dashed: boolean, children: React.ReactNode) => (
    <div
      data-testid={testId}
      className={cn(
        "rounded-lg border px-3 py-2 text-sm text-muted-foreground",
        dashed ? "border-dashed border-border" : "border-border"
      )}
    >
      {children}
    </div>
  );

  switch (outcome.kind) {
    case "not-forge-project":
      return shell(
        "forge-audit-not-project",
        false,
        <>
          {projectName ? <span className="font-mono">{projectName}</span> : "This project"} has no{" "}
          <span className="font-mono">forge.yaml</span>, so there is no audit to show. This is
          expected — most projects are not forge projects.
        </>
      );
    case "unsupported":
      return shell(
        "forge-audit-unsupported",
        false,
        <>
          forge{" "}
          <span className="font-mono text-foreground">
            {outcome.meta.forgeVersion || "on your daemon"}
          </span>{" "}
          cannot produce this audit
          {outcome.meta.unsupportedReason ? ` — ${outcome.meta.unsupportedReason}` : ""}. Your
          project has not changed; this build simply cannot read it.
        </>
      );
    case "unreachable":
      return shell(
        "forge-audit-unreachable",
        true,
        <>
          The audit could not be produced, so this project&apos;s health is unknown
          {outcome.meta.unreachableReason ? ` — ${outcome.meta.unreachableReason}` : ""}. This is
          not evidence of a problem.
        </>
      );
    case "malformed":
      return shell(
        "forge-audit-malformed",
        true,
        <>
          forge returned an audit this build of reliant could not parse, so nothing here should be
          read as a statement about the project.
        </>
      );
  }
}
