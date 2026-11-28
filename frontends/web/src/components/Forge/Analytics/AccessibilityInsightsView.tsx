// Copyright (c) 2026 Reliant Labs

/**
 * SITE INSIGHTS › ACCESSIBILITY (Site insights R5):
 * /forge/p/:app/site-insights/accessibility — what keeps people who use a
 * screen reader, a keyboard or a magnifier from using a hosted static site,
 * from the control plane's site audit after each release (useSiteAudit.ts),
 * whose headless browser runs axe-core on each rendered page:
 *
 *   header   the site's accessibility score, its delta against the previous
 *            release's audit, the issues by axe impact, Re-run
 *   routes   each route's score, worst first, and its issues by impact; a
 *            route filters the issues to it
 *   issues   one per axe rule, in Lighthouse's accessibility groups (names
 *            and labels, contrast, ARIA, navigation, …), most serious first:
 *            what the rule asks, its impact, WCAG criteria, the routes it is
 *            on and deque's help page; open one for each element's selector,
 *            HTML snippet (truncated by the auditor) and what axe says fails
 *
 * The site and environment are the SEO view's (useAuditTarget). The model:
 * accessibilityInsightsModel.ts.
 */

import { useMemo, useState } from "react";
import { ChevronRight, ExternalLink, X } from "lucide-react";

import Button from "@/components/forge-ui/button";
import Card from "@/components/forge-ui/card";
import Status, { type StatusTone } from "@/components/forge-ui/status";
import { cn } from "@/lib/utils";

import { SiteInsightsHeader } from "./siteInsightsParts";
import { AuditEmpty, AuditError, AuditHeaderCard, AuditSitePicker, AuditTargetNotice, ScoreCell } from "./siteAuditParts";
import type { AuditTarget, SiteAudit } from "./siteAuditModel";
import { A11Y_GROUP_LABEL, a11yIssues, a11yRoutes, IMPACT_LABEL, IMPACTS, impactCounts, issuesByGroup, type A11yIssue, type A11yRouteRow, type Impact } from "./accessibilityInsightsModel";
import { useAuditTarget, useSiteAudit } from "./useSiteAudit";

const IMPACT_TONE: Record<Impact, StatusTone> = { critical: "danger", serious: "danger", moderate: "warning", minor: "neutral" };
const IMPACT_INK: Record<Impact, string> = { critical: "text-danger-ink", serious: "text-danger-ink", moderate: "text-warning-ink", minor: "text-ink-muted" };

/** Site insights' Accessibility view: its header (with the tabs) and its body; SiteInsightsPage frames it. */
export function AccessibilityInsightsView() {
  const { target, sites, loading, setSite } = useAuditTarget();
  const site = target.kind === "site" ? target : null;
  const query = useSiteAudit(site);
  return (
    <>
      <SiteInsightsHeader view="accessibility" controls={<AuditSitePicker sites={sites} target={target} onChange={setSite} />} />
      {!site ? (
        <AuditTargetNotice target={target} loading={loading} />
      ) : query.isLoading ? (
        <div className="h-64 animate-pulse rounded-xl border border-border bg-surface motion-reduce:animate-none" aria-label="Loading the accessibility audit" />
      ) : query.error && !query.data ? (
        <AuditError error={query.error} frontend={site.frontend} onRetry={() => void query.refetch()} retrying={query.isFetching} />
      ) : !query.data?.audit ? (
        <AuditEmpty audit={query.data} envId={site.envId} frontend={site.frontend} />
      ) : (
        <AccessibilityInsightsBody audit={query.data} target={site} />
      )}
    </>
  );
}

/** The view over one finished audit. */
export function AccessibilityInsightsBody({ audit, target }: { audit: SiteAudit; target: Extract<AuditTarget, { kind: "site" }> }) {
  const [route, setRoute] = useState<string | null>(null);
  const routes = useMemo(() => a11yRoutes(audit), [audit]);
  const all = useMemo(() => a11yIssues(audit), [audit]);
  const shown = useMemo(() => (route === null ? all : a11yIssues(audit, route)), [audit, all, route]);
  const counts = impactCounts(all);
  return (
    <>
      <AuditHeaderCard audit={audit} category="accessibility" envId={target.envId} envName={target.envName} frontend={target.frontend}>
        <dl className="flex gap-6 text-ui" data-testid="site-audit-a11y-counts">
          {IMPACTS.map((impact) => (
            <div key={impact}>
              <dt className="text-meta text-ink-muted">{IMPACT_LABEL[impact]}</dt>
              <dd className={cn("text-2xl font-semibold tabular-nums", counts[impact] ? IMPACT_INK[impact] : "text-ink")}>{counts[impact]}</dd>
            </div>
          ))}
        </dl>
      </AuditHeaderCard>
      {audit.audit && audit.audit.pagesRendered === 0 ? (
        <Card className="max-w-[var(--form-max)]" data-testid="site-audit-a11y-not-rendered">
          <p className="text-ui text-ink-muted">This audit rendered no page, so accessibility was not checked. It is checked in a headless browser on each audit that renders pages.</p>
        </Card>
      ) : (
        <>
          <RoutesTable rows={routes} selected={route} onSelect={(next) => setRoute(next === route ? null : next)} />
          <Issues issues={shown} route={route} onClearRoute={() => setRoute(null)} />
        </>
      )}
    </>
  );
}

const TH = "h-9 px-3 text-meta font-medium text-ink-muted whitespace-nowrap";
const TD = "h-11 px-3 text-ui";

function RoutesTable({ rows, selected, onSelect }: { rows: A11yRouteRow[]; selected: string | null; onSelect: (route: string) => void }) {
  return (
    <section className="space-y-2" aria-labelledby="site-audit-a11y-routes-title">
      <h2 id="site-audit-a11y-routes-title" className="text-section text-ink">
        Routes <span className="ml-2 text-meta font-normal text-ink-muted">worst score first · pick one to see only its issues</span>
      </h2>
      {rows.length === 0 ? (
        <p className="text-ui text-ink-muted">The audit read no route.</p>
      ) : (
        <div className="overflow-x-auto rounded-xl border border-border bg-surface" data-testid="site-insights-a11y-routes">
          <table className="w-full min-w-[40rem] border-collapse">
            <thead>
              <tr className="border-b border-border">
                <th className={cn(TH, "text-left")}>Route</th>
                <th className={cn(TH, "text-right")} aria-sort="ascending">
                  Score
                </th>
                {IMPACTS.map((impact) => (
                  <th key={impact} className={cn(TH, "text-right")}>
                    {IMPACT_LABEL[impact]}
                  </th>
                ))}
              </tr>
            </thead>
            <tbody>
              {rows.map((row) => (
                <tr
                  key={row.route}
                  className={cn("cursor-pointer border-b border-border-subtle last:border-b-0 hover:bg-ink/5", row.route === selected && "bg-ink/5")}
                  onClick={() => onSelect(row.route)}
                  data-testid="site-audit-a11y-route"
                >
                  <td className={TD}>
                    <button
                      type="button"
                      aria-pressed={row.route === selected}
                      className="font-mono text-code text-ink hover:underline focus-visible:ring-2 focus-visible:ring-ring"
                      onClick={(e) => {
                        e.stopPropagation();
                        onSelect(row.route);
                      }}
                    >
                      {row.route}
                    </button>
                    {!row.rendered && <span className="ml-2 text-meta text-ink-muted">not rendered</span>}
                  </td>
                  <td className={cn(TD, "text-right")}>
                    <ScoreCell score={row.score} />
                  </td>
                  {IMPACTS.map((impact) => (
                    <td key={impact} className={cn(TD, "text-right tabular-nums", row.counts[impact] ? IMPACT_INK[impact] : "text-ink-muted")}>
                      {row.counts[impact] || "—"}
                    </td>
                  ))}
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

function Issues({ issues, route, onClearRoute }: { issues: A11yIssue[]; route: string | null; onClearRoute: () => void }) {
  const sections = issuesByGroup(issues);
  return (
    <section className="space-y-3" aria-labelledby="site-audit-a11y-issues-title">
      <div className="flex flex-wrap items-center gap-2">
        <h2 id="site-audit-a11y-issues-title" className="text-section text-ink">
          Issues <span className="ml-2 text-meta font-normal text-ink-muted">by Lighthouse&apos;s accessibility groups, most serious first</span>
        </h2>
        {route !== null && (
          <Button variant="ghost" size="xs" rightIcon={<X />} onClick={onClearRoute} data-testid="site-audit-a11y-route-filter">
            On <span className="font-mono">{route}</span>
          </Button>
        )}
      </div>
      {sections.length === 0 ? (
        <p className="text-ui text-ink-muted" data-testid="site-audit-a11y-no-issues">
          {route === null ? "axe found no accessibility issue on the pages it checked." : "axe found no accessibility issue on this route."}
        </p>
      ) : (
        sections.map((section) => (
          <div key={section.group} className="space-y-1.5" data-testid={`site-audit-a11y-group-${section.group}`}>
            <h3 className="text-ui font-medium text-ink">
              {A11Y_GROUP_LABEL[section.group].title} <span className="text-meta font-normal text-ink-muted">{A11Y_GROUP_LABEL[section.group].about}</span>
            </h3>
            <ul className="divide-y divide-border-subtle rounded-xl border border-border bg-surface">
              {section.issues.map((issue) => (
                <IssueRow key={issue.rule} issue={issue} />
              ))}
            </ul>
          </div>
        ))
      )}
    </section>
  );
}

function IssueRow({ issue }: { issue: A11yIssue }) {
  const [open, setOpen] = useState(false);
  const elements = issue.nodes.length + issue.omitted;
  return (
    <li data-testid="site-audit-a11y-issue">
      <button
        type="button"
        aria-expanded={open}
        onClick={() => setOpen((v) => !v)}
        className="flex w-full items-start gap-3 px-4 py-3 text-left hover:bg-ink/5 focus-visible:ring-2 focus-visible:ring-inset focus-visible:ring-ring"
      >
        <ChevronRight className={cn("mt-0.5 size-4 shrink-0 text-ink-muted transition-transform motion-reduce:transition-none", open && "rotate-90")} aria-hidden="true" />
        <span className="min-w-0 flex-1 space-y-1">
          <span className="flex flex-wrap items-center gap-x-3 gap-y-1">
            <Status tone={IMPACT_TONE[issue.impact]} label={IMPACT_LABEL[issue.impact]} size="sm" />
            <span className="text-ui font-medium text-ink">{issue.help}</span>
          </span>
          <span className="flex flex-wrap items-center gap-x-3 gap-y-1 text-meta text-ink-muted">
            <span className="font-mono">{issue.rule}</span>
            {issue.wcag.length > 0 && <span>{issue.wcag.join(" · ")}</span>}
            <span>
              {elements} {elements === 1 ? "element" : "elements"} on {issue.routes.length === 1 ? <span className="font-mono">{issue.routes[0] || "/"}</span> : `${issue.routes.length} routes`}
            </span>
          </span>
        </span>
      </button>
      {open && (
        <div className="space-y-3 px-4 pb-4 pl-11" data-testid="site-audit-a11y-issue-detail">
          <p className="text-meta text-ink-muted">
            Routes: <span className="font-mono text-ink">{issue.routes.map((r) => r || "/").join(", ")}</span>
            {issue.helpUrl && (
              <>
                {" · "}
                <a href={issue.helpUrl} target="_blank" rel="noopener noreferrer" className="inline-flex items-center gap-1 text-primary-ink hover:underline" data-testid="site-audit-a11y-help">
                  How to fix it (Deque)
                  <ExternalLink className="size-3" aria-hidden="true" />
                </a>
              </>
            )}
          </p>
          <ul className="space-y-2">
            {issue.nodes.map((node, i) => (
              <li key={`${node.route} ${node.target} ${i}`} className="space-y-1 rounded-md border border-border/60 bg-surface-sunken p-3" data-testid="site-audit-a11y-node">
                <p className="flex flex-wrap items-baseline gap-x-2 text-meta text-ink-muted">
                  <span className="font-mono text-ink">{node.target}</span>
                  <span>on {node.route || "/"}</span>
                </p>
                {node.html && <code className="block whitespace-pre-wrap break-all font-mono text-code text-ink">{node.html}</code>}
                {node.summary && <p className="text-meta text-ink-muted">{node.summary}</p>}
              </li>
            ))}
          </ul>
          {issue.omitted > 0 && <p className="text-meta text-ink-muted">and {issue.omitted} more elements the audit did not keep</p>}
        </div>
      )}
    </li>
  );
}
