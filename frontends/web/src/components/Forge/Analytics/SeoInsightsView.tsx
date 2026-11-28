// Copyright (c) 2026 Reliant Labs

/**
 * SITE INSIGHTS › SEO (Site insights R4): /forge/p/:app/site-insights/seo —
 * how a hosted static site reads to search engines and link previews, from
 * the control plane's site audit after each release (useSiteAudit.ts):
 *
 *   header        the site's SEO score, its delta against the previous
 *                 release's audit, when and of which release, Re-run with
 *                 the org's re-runs left today
 *   site checks   robots.txt, sitemap.xml, indexing (expected against
 *                 actual: a preview's noindex is by design), the canonical
 *                 host, hreflang, structured data
 *   routes        every crawled route, worst score first: its title and
 *                 description (with their lengths), canonical, Open Graph,
 *                 indexability, JSON-LD types; a route opens its drawer
 *   route drawer  every finding with its evidence and the fix, and the
 *                 link preview (SocialCardPreview: Slack, X, LinkedIn)
 *   broken links  source route, target, status or error, internal or not
 *
 * The site is the URL's `?site=` (siteInsightsParts.tsx), the environment
 * the console's scope (prod first with every environment in scope). Before a
 * site's first audit: "Your first audit runs after the next release".
 * The model: seoInsightsModel.ts.
 */

import { useMemo, useState, type ReactNode } from "react";
import { Check, X } from "lucide-react";

import Card from "@/components/forge-ui/card";
import Modal from "@/components/forge-ui/modal";
import Status, { type StatusTone } from "@/components/forge-ui/status";
import { cn } from "@/lib/utils";

import { SiteInsightsHeader } from "./siteInsightsParts";
import { AuditEmpty, AuditError, AuditHeaderCard, AuditSitePicker, AuditTargetNotice, ScoreCell } from "./siteAuditParts";
import { findingsOf, type AuditFinding, type AuditTarget, type SeoSeverity, type SiteAudit } from "./siteAuditModel";
import {
  brokenLinks,
  seoCodeLabel,
  seoRoutes,
  seoSeverityOf,
  SEO_SEVERITY_LABEL,
  siteChecks,
  siteWideFindings,
  socialCardOf,
  type CheckState,
  type SeoRouteRow,
  type SiteCheck,
} from "./seoInsightsModel";
import { SocialCardPreview } from "./SocialCardPreview";
import { useAuditTarget, useSiteAudit } from "./useSiteAudit";

const SEVERITY_TONE: Record<SeoSeverity, StatusTone> = { error: "danger", warn: "warning", info: "neutral" };
const CHECK_TONE: Record<CheckState, StatusTone> = { pass: "success", info: "neutral", warn: "warning", fail: "danger", none: "neutral" };
const CHECK_WORD: Record<CheckState, string> = { pass: "Passed", info: "Advice", warn: "Warning", fail: "Error", none: "Not used" };

/** Site insights' SEO view: its header (with the tabs) and its body; SiteInsightsPage frames it. */
export function SeoInsightsView() {
  const { target, sites, loading, setSite } = useAuditTarget();
  const site = target.kind === "site" ? target : null;
  const query = useSiteAudit(site);
  return (
    <>
      <SiteInsightsHeader view="seo" controls={<AuditSitePicker sites={sites} target={target} onChange={setSite} />} />
      {!site ? (
        <AuditTargetNotice target={target} loading={loading} />
      ) : query.isLoading ? (
        <div className="h-64 animate-pulse rounded-xl border border-border bg-surface motion-reduce:animate-none" aria-label="Loading the SEO audit" />
      ) : query.error && !query.data ? (
        <AuditError error={query.error} frontend={site.frontend} onRetry={() => void query.refetch()} retrying={query.isFetching} />
      ) : !query.data?.audit ? (
        <AuditEmpty audit={query.data} envId={site.envId} frontend={site.frontend} />
      ) : (
        <SeoInsightsBody audit={query.data} target={site} />
      )}
    </>
  );
}

/** The view over one finished audit. */
export function SeoInsightsBody({ audit, target }: { audit: SiteAudit; target: Extract<AuditTarget, { kind: "site" }> }) {
  const checks = useMemo(() => siteChecks(audit, target.preview), [audit, target.preview]);
  const routes = useMemo(() => seoRoutes(audit), [audit]);
  const links = useMemo(() => brokenLinks(audit), [audit]);
  const covered = new Set(checks.flatMap((check) => check.findings));
  const otherSiteWide = siteWideFindings(audit).filter((f) => !covered.has(f));
  const seo = findingsOf(audit, "seo");
  const errors = seo.filter((f) => seoSeverityOf(f) === "error").length;
  const warnings = seo.filter((f) => seoSeverityOf(f) === "warn").length;
  const [open, setOpen] = useState<string | null>(null);
  const openRow = routes.find((row) => row.route === open) ?? null;
  return (
    <>
      <AuditHeaderCard audit={audit} category="seo" envId={target.envId} envName={target.envName} frontend={target.frontend}>
        <dl className="flex gap-6 text-ui" data-testid="site-audit-seo-counts">
          <Figure label="Errors" value={errors} tone={errors ? "text-danger-ink" : undefined} />
          <Figure label="Warnings" value={warnings} tone={warnings ? "text-warning-ink" : undefined} />
          <Figure label="Broken links" value={links.length} tone={links.length ? "text-danger-ink" : undefined} />
        </dl>
      </AuditHeaderCard>
      <SiteChecks checks={checks} other={otherSiteWide} />
      <RoutesTable rows={routes} onOpen={setOpen} />
      <BrokenLinks links={links} />
      <RouteDrawer row={openRow} onClose={() => setOpen(null)} />
    </>
  );
}

function Figure({ label, value, tone }: { label: string; value: number; tone?: string }) {
  return (
    <div>
      <dt className="text-meta text-ink-muted">{label}</dt>
      <dd className={cn("text-2xl font-semibold tabular-nums", tone ?? "text-ink")}>{value}</dd>
    </div>
  );
}

function SiteChecks({ checks, other }: { checks: SiteCheck[]; other: AuditFinding[] }) {
  return (
    <section className="space-y-2" aria-labelledby="site-audit-checks-title">
      <h2 id="site-audit-checks-title" className="text-section text-ink">
        Site checks
      </h2>
      <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3" data-testid="site-insights-seo-checks">
        {checks.map((check) => (
          <Card key={check.id} padding="sm" className="min-w-0 space-y-1.5" data-testid={`site-audit-check-${check.id}`}>
            <div className="flex items-center justify-between gap-2">
              <h3 className="text-ui font-medium text-ink">{check.label}</h3>
              <Status tone={CHECK_TONE[check.state]} label={CHECK_WORD[check.state]} size="sm" />
            </div>
            <p className="truncate text-ui text-ink-muted" title={check.value}>
              {check.value}
            </p>
            {check.findings.slice(0, 3).map((f, i) => (
              <p key={i} className="text-meta text-ink-muted">
                <span className="text-ink">{seoCodeLabel(f.code)}</span>
                {f.route ? <span className="font-mono"> {f.route}</span> : null}: {f.fix}
              </p>
            ))}
            {check.findings.length > 3 && <p className="text-meta text-ink-muted">and {check.findings.length - 3} more</p>}
          </Card>
        ))}
      </div>
      {other.length > 0 && (
        <Card padding="sm" data-testid="site-audit-site-wide">
          <FindingList findings={other} />
        </Card>
      )}
    </section>
  );
}

const TH = "h-9 px-3 text-meta font-medium text-ink-muted whitespace-nowrap";
const TD = "h-11 px-3 text-ui";

function Present({ ok, label }: { ok: boolean | null; label: string }) {
  if (ok === null) return <span className="text-ink-muted">—</span>;
  return ok ? <Check className="size-4 text-success-ink" aria-label={`${label}: yes`} /> : <X className="size-4 text-danger-ink" aria-label={`${label}: no`} />;
}

function Length({ text, length }: { text: string; length: number }) {
  if (!text) return <span className="text-danger-ink">missing</span>;
  return (
    <span className="flex min-w-0 items-baseline gap-1.5">
      <span className="truncate text-ink" title={text}>
        {text}
      </span>
      <span className="shrink-0 text-meta tabular-nums text-ink-muted">{length}</span>
    </span>
  );
}

function RoutesTable({ rows, onOpen }: { rows: SeoRouteRow[]; onOpen: (route: string) => void }) {
  return (
    <section className="space-y-2" aria-labelledby="site-audit-routes-title">
      <h2 id="site-audit-routes-title" className="text-section text-ink">
        Routes <span className="ml-2 text-meta font-normal text-ink-muted">worst score first · open one for its findings and link preview</span>
      </h2>
      {rows.length === 0 ? (
        <p className="text-ui text-ink-muted">The audit read no route.</p>
      ) : (
        <div className="overflow-x-auto rounded-xl border border-border bg-surface" data-testid="site-insights-seo-routes">
          <table className="w-full min-w-[64rem] table-fixed border-collapse">
            <colgroup>
              <col className="w-44" />
              <col className="w-20" />
              <col />
              <col />
              <col className="w-20" />
              <col className="w-16" />
              <col className="w-24" />
              <col className="w-40" />
              <col className="w-28" />
            </colgroup>
            <thead>
              <tr className="border-b border-border">
                <th className={cn(TH, "text-left")}>Route</th>
                <th className={cn(TH, "text-right")} aria-sort="ascending">
                  Score
                </th>
                <th className={cn(TH, "text-left")}>Title</th>
                <th className={cn(TH, "text-left")}>Description</th>
                <th className={cn(TH, "text-center")}>Canonical</th>
                <th className={cn(TH, "text-center")}>OG</th>
                <th className={cn(TH, "text-center")}>Indexable</th>
                <th className={cn(TH, "text-left")}>JSON-LD</th>
                <th className={cn(TH, "text-right")}>Findings</th>
              </tr>
            </thead>
            <tbody>
              {rows.map((row) => (
                <tr key={row.route} className="cursor-pointer border-b border-border-subtle last:border-b-0 hover:bg-ink/5" onClick={() => onOpen(row.route)} data-testid="site-audit-seo-route">
                  <td className={cn(TD, "max-w-0")}>
                    <button
                      type="button"
                      className="block max-w-full truncate font-mono text-code text-ink hover:underline focus-visible:ring-2 focus-visible:ring-ring"
                      onClick={(e) => {
                        e.stopPropagation();
                        onOpen(row.route);
                      }}
                    >
                      {row.route}
                    </button>
                    {row.status !== 200 && <span className="text-meta text-danger-ink">{row.status === 0 ? "no response" : `HTTP ${row.status}`}</span>}
                  </td>
                  <td className={cn(TD, "text-right")}>
                    <ScoreCell score={row.score} />
                  </td>
                  <td className={cn(TD, "max-w-0")}>
                    <Length text={row.title} length={row.titleLength} />
                  </td>
                  <td className={cn(TD, "max-w-0")}>
                    <Length text={row.description} length={row.descriptionLength} />
                  </td>
                  <td className={cn(TD, "text-center")}>
                    <span className="inline-flex justify-center" title={row.canonical || "No canonical"}>
                      <Present ok={row.status >= 200 && row.status < 300 ? !!row.canonical : null} label="Canonical" />
                    </span>
                  </td>
                  <td className={cn(TD, "text-center")}>
                    <span className="inline-flex justify-center">
                      <Present ok={row.ogComplete} label="Open Graph complete" />
                    </span>
                  </td>
                  <td className={cn(TD, "text-center")}>
                    <span className="inline-flex justify-center">
                      <Present ok={row.indexable} label="Indexable" />
                    </span>
                  </td>
                  <td className={cn(TD, "max-w-0 truncate text-ink-muted")} title={row.jsonldTypes.join(", ")}>
                    {row.jsonldTypes.length ? row.jsonldTypes.join(", ") : "—"}
                  </td>
                  <td className={cn(TD, "text-right tabular-nums")}>
                    {row.findings.length === 0 ? (
                      <span className="text-ink-muted">none</span>
                    ) : (
                      <span className="inline-flex items-center justify-end gap-2">
                        {row.errors > 0 && <Status tone="danger" label={String(row.errors)} size="sm" />}
                        {row.warnings > 0 && <Status tone="warning" label={String(row.warnings)} size="sm" />}
                        {row.findings.length - row.errors - row.warnings > 0 && <Status tone="neutral" label={String(row.findings.length - row.errors - row.warnings)} size="sm" />}
                      </span>
                    )}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

function BrokenLinks({ links }: { links: ReturnType<typeof brokenLinks> }) {
  return (
    <section className="space-y-2" aria-labelledby="site-audit-links-title">
      <h2 id="site-audit-links-title" className="text-section text-ink">
        Broken links
      </h2>
      {links.length === 0 ? (
        <p className="text-ui text-ink-muted" data-testid="site-audit-no-broken-links">
          Every link the audit checked answered.
        </p>
      ) : (
        <div className="overflow-x-auto rounded-xl border border-border bg-surface" data-testid="site-audit-broken-links">
          <table className="w-full min-w-[40rem] border-collapse">
            <thead>
              <tr className="border-b border-border">
                <th className={cn(TH, "text-left")}>On</th>
                <th className={cn(TH, "text-left")}>Links to</th>
                <th className={cn(TH, "text-left")}>Answer</th>
                <th className={cn(TH, "text-left")}>Kind</th>
              </tr>
            </thead>
            <tbody>
              {links.map((link) => (
                <tr key={`${link.source} ${link.target}`} className="border-b border-border-subtle last:border-b-0">
                  <td className={cn(TD, "font-mono text-code text-ink")}>{link.source || "/"}</td>
                  <td className={cn(TD, "max-w-0 truncate font-mono text-code text-ink")} title={link.target}>
                    {link.target}
                  </td>
                  <td className={cn(TD, "text-danger-ink")}>{link.status ? `HTTP ${link.status}` : link.error || "no answer"}</td>
                  <td className={cn(TD, "text-ink-muted")}>{link.internal ? "Internal" : "External"}</td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

/** Findings, each with its severity, what it is about, its evidence and the fix. */
function FindingList({ findings, empty }: { findings: readonly AuditFinding[]; empty?: ReactNode }) {
  if (findings.length === 0) return <>{empty ?? null}</>;
  return (
    <ul className="divide-y divide-border-subtle" data-testid="site-audit-findings">
      {findings.map((f, i) => {
        const severity = seoSeverityOf(f);
        const evidence = Object.entries(f.evidence).filter(([, value]) => value !== "");
        return (
          <li key={`${f.code} ${f.ref} ${i}`} className="space-y-1 py-2.5 first:pt-0 last:pb-0" data-testid="site-audit-finding">
            <div className="flex flex-wrap items-center gap-x-2 gap-y-1">
              <Status tone={SEVERITY_TONE[severity]} label={SEO_SEVERITY_LABEL[severity]} size="sm" />
              <span className="text-ui font-medium text-ink">{seoCodeLabel(f.code)}</span>
              {f.ref && <span className="truncate font-mono text-code text-ink-muted">{f.ref}</span>}
            </div>
            {evidence.length > 0 && (
              <p className="text-meta text-ink-muted">
                {evidence.map(([key, value]) => (
                  <span key={key} className="mr-3">
                    {key.replace(/_/g, " ")}: <span className="font-mono text-ink">{value}</span>
                  </span>
                ))}
              </p>
            )}
            <p className="text-ui text-ink">{f.fix}</p>
          </li>
        );
      })}
    </ul>
  );
}

/** One route: its scores and head facts, every finding with the fix, and its link preview. */
function RouteDrawer({ row, onClose }: { row: SeoRouteRow | null; onClose: () => void }) {
  return (
    <Modal
      open={!!row}
      onClose={onClose}
      title={row ? <span className="font-mono">{row.route}</span> : undefined}
      description={row ? `SEO score ${row.score ?? "—"} · ${row.status ? `HTTP ${row.status}` : "no response"}` : undefined}
      size="lg"
      sheetOnMobile
      // A drawer on the right on a wide screen (the routes stay in view behind it); a sheet on a phone.
      overlayClassName="sm:justify-end sm:p-0!"
      className="sm:h-full sm:max-h-none! sm:rounded-none! sm:border-l sm:border-border"
    >
      {row && (
        <div className="space-y-6" data-testid="site-audit-route-drawer">
          <section className="space-y-2" aria-labelledby="site-audit-route-findings">
            <h3 id="site-audit-route-findings" className="text-section text-ink">
              Findings
            </h3>
            <FindingList findings={row.findings} empty={<p className="text-ui text-ink-muted">Nothing to fix on this route.</p>} />
          </section>
          <SocialCardPreview card={socialCardOf(row.page)} />
          <section className="space-y-2" aria-labelledby="site-audit-route-head">
            <h3 id="site-audit-route-head" className="text-section text-ink">
              Head
            </h3>
            <dl className="grid grid-cols-[8rem_minmax(0,1fr)] gap-x-3 gap-y-1.5 text-ui">
              {[
                ["Title", row.page.head.title],
                ["Description", row.page.head.description],
                ["Canonical", row.page.head.canonical],
                ["Robots", [row.page.head.robots, row.page.head.xRobotsTag && `X-Robots-Tag: ${row.page.head.xRobotsTag}`].filter(Boolean).join(" · ")],
                ["Language", row.page.head.lang],
                ["JSON-LD", row.page.head.jsonldTypes.join(", ")],
                ["hreflang", row.page.head.hreflang.map((alt) => alt.hreflang).join(", ")],
                ["Set by JavaScript", row.page.head.clientRendered.join(", ")],
              ].map(([label, value]) => (
                <div key={label} className="contents">
                  <dt className="text-ink-muted">{label}</dt>
                  <dd className="min-w-0 break-words text-ink">{value || <span className="text-ink-muted">—</span>}</dd>
                </div>
              ))}
            </dl>
          </section>
        </div>
      )}
    </Modal>
  );
}
