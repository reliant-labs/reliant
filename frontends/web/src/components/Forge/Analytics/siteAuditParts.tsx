// Copyright (c) 2026 Reliant Labs

/**
 * What Site insights' SEO and Accessibility views share (R4/R5): the site
 * picker over the project's hosted static sites, the states before there is
 * an audit to show, and the audit's headline card (the score, its delta
 * against the previous release, when and of which release, and Re-run with
 * the org's re-runs left today).
 */

import type { ReactNode } from "react";
import { Code, ConnectError } from "@connectrpc/connect";
import { RefreshCw, ScanSearch } from "lucide-react";

import Button from "@/components/forge-ui/button";
import Card from "@/components/forge-ui/card";
import Status from "@/components/forge-ui/status";
import { cn } from "@/lib/utils";
import { useObservabilityOrg } from "@/hooks/observability-queries";

import { VariableSelect } from "../Observability/Dashboards/parts";
import { formatAgo, formatIn } from "../Observability/format";
import { useObsReadOnly } from "../Observability/obsReadOnly";
import { ObsError } from "../Observability/ObsStates";
import { formatDelta, inProgressWords, scoreDelta, categoryScore, type AuditCategory, type AuditSite, type AuditTarget, type SiteAudit } from "./siteAuditModel";
import { rateScore, type Rating } from "./speedInsightsModel";
import { useRunSiteAudit, useSiteAuditScores } from "./useSiteAudit";

/** A rating's ink (text) and stroke (the ring): semantic status tokens. */
export const SCORE_INK: Record<Rating, string> = { good: "text-success-ink", ni: "text-warning-ink", poor: "text-danger-ink" };
const STROKE: Record<Rating, string> = { good: "stroke-success", ni: "stroke-warning", poor: "stroke-danger" };

const CATEGORY_LABEL: Record<AuditCategory, string> = { seo: "SEO score", accessibility: "Accessibility score" };

/** Which hosted static site the view audits; also lets an invalid selection recover. */
export function AuditSitePicker({ sites, target, onChange }: { sites: readonly AuditSite[]; target: AuditTarget; onChange: (frontend: string | undefined) => void }) {
  if (sites.length < 2 && target.kind !== "not-static" && target.kind !== "not-in-env") return null;
  const value = target.kind === "site" || target.kind === "not-in-env" ? target.frontend : target.kind === "not-static" ? target.site : "";
  const options = [{ value: "", label: "All sites" }, ...(target.kind === "not-static" ? [{ value, label: value }] : []), ...sites.map((site) => ({ value: site.frontend, label: site.frontend }))];
  return <VariableSelect id="site-insights-audit-site" label="site" value={value} options={options} onChange={(next) => onChange(next || undefined)} />;
}

/** A score as a coloured ring (the Speed view's gauge). */
export function AuditScoreRing({ score, label, testId }: { score: number | null; label: string; testId?: string }) {
  const rating = rateScore(score);
  const r = 42;
  const circumference = 2 * Math.PI * r;
  const filled = score === null ? 0 : (score / 100) * circumference;
  return (
    <div className="relative size-24 shrink-0" role="img" aria-label={score === null ? `${label}: not scored` : `${label} ${score} of 100`}>
      <svg viewBox="0 0 100 100" className="size-full -rotate-90" aria-hidden="true">
        <circle cx="50" cy="50" r={r} fill="none" strokeWidth="8" className="stroke-ink/10" />
        {rating && <circle cx="50" cy="50" r={r} fill="none" strokeWidth="8" strokeLinecap="round" strokeDasharray={`${filled} ${circumference}`} className={STROKE[rating]} />}
      </svg>
      <span className={cn("absolute inset-0 flex items-center justify-center text-figure tabular-nums", rating ? SCORE_INK[rating] : "text-ink-muted")} data-testid={testId}>
        {score ?? "—"}
      </span>
    </div>
  );
}

/** A route's score in a table cell. */
export function ScoreCell({ score }: { score: number | null }) {
  const rating = rateScore(score);
  return <span className={cn("inline-flex min-w-9 justify-center text-ui font-semibold tabular-nums", rating ? SCORE_INK[rating] : "text-ink-muted")}>{score ?? "—"}</span>;
}

/**
 * One site's audit score as a summary card shows it (the static site's page,
 * Services/SiteDetail): the figure, coloured, with its delta against the
 * previous release, or what is pending before the first audit.
 */
export function SiteAuditScoreSummary({ envId, frontend, category }: { envId: string; frontend: string; category: AuditCategory }) {
  const scores = useSiteAuditScores(envId && frontend ? { envId, frontend } : null);
  const { score, delta } = scores[category];
  if (scores.isLoading) return <div className="mt-1 h-7 w-16 animate-pulse rounded-md bg-ink/5 motion-reduce:animate-none" aria-label="Loading the audit score" />;
  const rating = rateScore(score);
  return (
    <div data-testid={`site-audit-summary-${category}`}>
      <div className={cn("mt-0.5 text-2xl font-semibold tabular-nums", rating ? SCORE_INK[rating] : "text-ink-muted")}>{score ?? "—"}</div>
      <p className="text-meta text-ink-muted">
        {score === null
          ? scores.audit?.audit
            ? "Not scored in the newest audit"
            : "Your first audit runs after the next release"
          : delta !== null && scores.audit?.previous
            ? `${formatDelta(delta)} since ${scores.audit.previous.release || "the previous audit"}`
            : `Release ${scores[category].release || "audited"}`}
      </p>
    </div>
  );
}

/** What a view shows instead of an audit: no static site, a site the scope does not deploy, a failed read. */
export function AuditTargetNotice({ target, loading }: { target: AuditTarget; loading: boolean }) {
  if (loading) return <div className="h-64 animate-pulse rounded-xl border border-border bg-surface motion-reduce:animate-none" aria-label="Loading the site audit" />;
  const copy =
    target.kind === "no-site"
      ? { title: "No hosted static site to audit", body: "Site insights audits each static site this project hosts on Reliant, after every release. Deploy a static frontend and its first audit runs after the release." }
      : target.kind === "not-static"
        ? { title: `${target.site} is not a hosted static site`, body: "Audits read the hosted static sites of the project. Choose one of them, or All sites, in the site picker." }
        : target.kind === "not-in-env"
          ? { title: `${target.frontend} is not deployed in ${target.env}`, body: "Choose an environment the site is deployed in, or every environment." }
          : null;
  if (!copy) return null;
  return (
    <Card className="flex max-w-[var(--form-max)] items-start gap-3" data-testid="site-audit-no-target">
      <span className="mt-0.5 text-ink-muted [&>svg]:size-5">
        <ScanSearch aria-hidden="true" />
      </span>
      <div className="space-y-1">
        <h2 className="text-section text-ink">{copy.title}</h2>
        <p className="text-ui text-ink-muted">{copy.body}</p>
      </div>
    </Card>
  );
}

/** The read failed. */
export function AuditError({ error, frontend, onRetry, retrying }: { error: unknown; frontend: string; onRetry: () => void; retrying: boolean }) {
  return <ObsError error={error} subject={frontend} onRetry={onRetry} retrying={retrying} />;
}

/** Before the site's first finished audit. */
export function AuditEmpty({ audit, envId, frontend }: { audit: SiteAudit | undefined; envId: string; frontend: string }) {
  const running = audit?.inProgress;
  return (
    <Card className="flex max-w-[var(--form-max)] flex-wrap items-start justify-between gap-3" data-testid="site-audit-empty">
      <div className="flex items-start gap-3">
        <span className="mt-0.5 text-ink-muted [&>svg]:size-5">
          <ScanSearch aria-hidden="true" />
        </span>
        <div className="space-y-1">
          <h2 className="text-section text-ink">Your first audit runs after the next release</h2>
          <p className="text-ui text-ink-muted">
            {running
              ? inProgressWords(running)
              : `After every release, each page of ${frontend} is crawled the way a search engine and a link preview read it, and checked for accessibility. The scores and the fix for each finding appear here.`}
          </p>
        </div>
      </div>
      {!running && <RerunButton audit={audit} envId={envId} frontend={frontend} />}
    </Card>
  );
}

/** What a read-only console (the public demo) says beside a disabled Re-run. */
export const RERUN_READ_ONLY_WORDS = "In a live project an audit runs after every release";

/**
 * Re-run the audit now, with the org's re-runs left today; disabled while one
 * is queued or running, or with none left. A read-only console (the demo,
 * useObsReadOnly) never sends it: RunSiteAudit is a write, so the button is
 * disabled with the reason and says when audits run instead.
 */
export function RerunButton({ audit, envId, frontend }: { audit: SiteAudit | undefined; envId: string; frontend: string }) {
  const run = useRunSiteAudit();
  const { orgId } = useObservabilityOrg();
  const readOnly = useObsReadOnly();
  if (readOnly.readOnly) {
    return (
      <div className="flex flex-col items-end gap-1" data-testid="site-audit-rerun">
        <Button variant="secondary" size="sm" leftIcon={<RefreshCw />} disabled title={readOnly.reason}>
          Re-run
        </Button>
        <span className="max-w-60 text-right text-meta text-ink-muted">{RERUN_READ_ONLY_WORDS}.</span>
      </div>
    );
  }
  const quota = audit?.quota ?? null;
  const busy = !!audit?.inProgress || run.isPending;
  const spent = !!quota && quota.manualRemaining <= 0;
  const left = quota ? `${quota.manualRemaining} of ${quota.manualPerDay} re-runs left today` : null;
  return (
    <div className="flex flex-col items-end gap-1" data-testid="site-audit-rerun">
      <Button
        variant="secondary"
        size="sm"
        leftIcon={<RefreshCw />}
        loading={run.isPending}
        disabled={busy || spent || !orgId}
        onClick={() => orgId && run.mutate({ environmentId: envId, frontend, orgId })}
        title={spent && quota?.resetsAtMs ? `Re-runs reset ${formatIn(quota.resetsAtMs)}` : undefined}
      >
        Re-run
      </Button>
      {left && <span className="text-meta text-ink-muted">{left}</span>}
      {run.error ? (
        <span role="alert" className="max-w-72 text-right text-meta text-danger-ink" data-testid="site-audit-rerun-error">
          {rerunErrorWords(run.error)}
        </span>
      ) : null}
    </div>
  );
}

/** Why a re-run was refused, in words: the server's own message, which for quota names the reset. */
export function rerunErrorWords(error: unknown): string {
  const err = ConnectError.from(error);
  if (err.code === Code.ResourceExhausted) return "No re-runs are left today. An audit still runs after every release.";
  return err.rawMessage || "The re-run could not be queued.";
}

/**
 * The audit's headline: the category's score with its delta against the
 * previous release's audit, when it ran and of which release, how many pages
 * it read, and Re-run. `children` are the view's own figures beside it.
 */
export function AuditHeaderCard({
  audit,
  category,
  envId,
  envName,
  frontend,
  children,
}: {
  audit: SiteAudit;
  category: AuditCategory;
  envId: string;
  envName: string;
  frontend: string;
  children?: ReactNode;
}) {
  const run = audit.audit!;
  const score = categoryScore(run, category);
  const previousScore = categoryScore(audit.previous, category);
  const delta = scoreDelta(score, previousScore);
  const rating = rateScore(score);
  return (
    <Card padding="none" className="grid min-w-0 gap-0 lg:grid-cols-[minmax(0,1fr)_auto]" data-testid={`site-audit-${category}-header`}>
      <div className="flex min-w-0 flex-wrap items-center gap-5 px-5 py-4">
        <AuditScoreRing score={score} label={CATEGORY_LABEL[category]} testId={`site-audit-${category}-score`} />
        <div className="min-w-0 space-y-1">
          <h2 className="text-ui font-medium text-ink-muted">{CATEGORY_LABEL[category]}</h2>
          <div className="flex flex-wrap items-center gap-x-3 gap-y-1 text-ui">
            {rating && <Status tone={rating === "good" ? "success" : rating === "ni" ? "warning" : "danger"} label={rating === "good" ? "Good" : rating === "ni" ? "Needs improvement" : "Poor"} />}
            {delta !== null && audit.previous && (
              <span className={cn("tabular-nums", delta < 0 ? "text-danger-ink" : delta > 0 ? "text-success-ink" : "text-ink-muted")} data-testid={`site-audit-${category}-delta`}>
                {formatDelta(delta)} since {audit.previous.release || "the previous audit"}
                <span className="text-ink-muted"> ({previousScore})</span>
              </span>
            )}
          </div>
          <p className="text-meta text-ink-muted" data-testid="site-audit-when">
            {[
              run.release ? `Release ${run.release}` : null,
              `${frontend} in ${envName}`,
              run.finishedAtMs !== null ? `audited ${formatAgo(run.finishedAtMs)}` : null,
              `${run.pagesFetched} ${run.pagesFetched === 1 ? "page" : "pages"} read`,
            ]
              .filter(Boolean)
              .join(" · ")}
          </p>
          <p className="text-meta text-ink-muted">Lighthouse-compatible scoring: the same audits and weights as Lighthouse&apos;s {category === "seo" ? "SEO" : "accessibility"} category.</p>
          {audit.inProgress && (
            <p className="text-meta text-ink-muted" data-testid="site-audit-in-progress">
              <Status tone="neutral" label={inProgressWords(audit.inProgress)} pulse size="sm" />
            </p>
          )}
          {run.status === "failed" && run.error && <p className="text-meta text-danger-ink">The newest audit failed: {run.error}</p>}
        </div>
        {children}
      </div>
      <div className="flex items-start justify-end border-border px-5 py-4 max-lg:border-t lg:border-l">
        <RerunButton audit={audit} envId={envId} frontend={frontend} />
      </div>
    </Card>
  );
}
