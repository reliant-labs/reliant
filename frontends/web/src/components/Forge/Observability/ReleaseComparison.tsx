// Copyright (c) 2025 Reliant Labs

/**
 * Before and after one promotion: did the release make things worse?
 *
 * The answer is control-plane's (ObservabilityService.GetReleaseHealth): the
 * two windows (each cut at the neighbouring promotions and at now), the
 * per-service figures over weighted server spans, the issues the release
 * opened or regressed, the alerts still firing, the workload restarts, the
 * findings against the thresholds (and where each threshold came from), and
 * the verdict — PASS, FAIL or INCONCLUSIVE. The same rules answer
 * `forge obs check` and alerting, so the console computes none of it: it asks
 * for a width and renders what comes back, including what the server could
 * NOT check (`not_evaluated`), so a PASS is never read as covering it.
 *
 * The one figure the server does not carry is resources per workload (CPU and
 * memory): those stay a panel query, over the server's own windows.
 *
 * The promotion is in the URL as environment + moment + id (deliveryScope.ts
 * compareParam), so a comparison is a link: "look what v1.8.3 did to checkout".
 */

import { useMemo, type ReactNode } from "react";
import { useQuery } from "@tanstack/react-query";
import { AlertTriangle, ArrowLeft, ArrowRight, Bell, CheckCircle2, CircleHelp, GitCommitHorizontal, RotateCw, XCircle } from "lucide-react";

import Card from "@/components/forge-ui/card";
import Link from "@/components/forge-ui/link";
import Button from "@/components/forge-ui/button";
import DataTable, { type DataTableColumn } from "@/components/forge-ui/data_table";
import { obsKeys, useReleaseHealth } from "@/hooks/observability-queries";
import {
  HEALTH_WINDOW_MS,
  HEALTH_WINDOWS,
  type HealthWindow,
  type ReleaseFinding,
  type ReleaseFindingCode,
  type ReleaseHealth,
  type ReleaseVerdict,
  type ServiceHealthDelta,
} from "@/services/controlPlane/observability/deliveryTypes";
import { obsRetry, releaseHealthErrorKind } from "@/services/controlPlane/observability/errors";
import { keepsLastResult } from "@/services/controlPlane/observability/quota";
import type { Issue } from "@/services/controlPlane/observability/issuesTypes";
import { useObsReadApi } from "@/hooks/observability-queries";
import type { ObsScope, Panel, PanelResult, TimeWindow } from "@/services/controlPlane/observability/types";
import { cn } from "@/lib/utils";

import { changeText, formatSpan, movement, shortCommit, type CompareRef, type Movement } from "./deliveryScope";
import { StatusBadge } from "./Errors/IssueBits";
import { formatAgo, formatBytes, formatCores, formatCount, formatDuration, formatPercent, formatRate, formatWhen } from "./format";
import type { ObsNavigate } from "./obsSearch";
import { ObsAsOf, ObsError, ObsLoadingRows } from "./ObsStates";

const WORKLOAD = "resource.attr.k8s.workload.name";

/** What GetReleaseHealth does not answer: CPU and memory per workload, one table per side. */
export const RESOURCES_PANEL: Panel = {
  id: "compare-resources",
  title: "Resource usage",
  kind: "table",
  signal: "metrics",
  series: [
    { label: "cpu", agg: "avg", field: "container.cpu.usage", metricType: "gauge" },
    { label: "memory", agg: "avg", field: "container.memory.working_set", metricType: "gauge" },
  ],
  groupBy: [WORKLOAD],
};

export interface WorkloadComparison {
  workload: string;
  cpu: [number | null, number | null];
  memory: [number | null, number | null];
}

export function compareWorkloads(before: PanelResult | undefined, after: PanelResult | undefined): WorkloadComparison[] {
  const read = (result: PanelResult | undefined) => {
    const [cpu, memory] = ["cpu", "memory"].map((label) => (result ? result.columns.indexOf(label) : -1));
    return new Map(
      (result?.rows ?? []).map((row) => [row.group[WORKLOAD] ?? "", { cpu: cpu >= 0 ? row.values[cpu] : null, memory: memory >= 0 ? row.values[memory] : null }])
    );
  };
  const b = read(before);
  const a = read(after);
  return [...new Set([...b.keys(), ...a.keys()])].sort().map((workload) => ({
    workload,
    cpu: [b.get(workload)?.cpu ?? null, a.get(workload)?.cpu ?? null],
    memory: [b.get(workload)?.memory ?? null, a.get(workload)?.memory ?? null],
  }));
}

function useResources(scope: ObsScope, side: "before" | "after", window: TimeWindow | null) {
  const api = useObsReadApi();
  const usable = !!window && window.endMs > window.startMs;
  return useQuery({
    queryKey: [...obsKeys.all(scope), "compare", RESOURCES_PANEL.id, side, window?.startMs ?? 0, window?.endMs ?? 0] as const,
    queryFn: () =>
      api.queryPanel({ ...scope, window: window!, panel: { ...RESOURCES_PANEL, id: `${RESOURCES_PANEL.id}-${side}` }, services: [], stepSeconds: 0, groupBy: RESOURCES_PANEL.groupBy }),
    enabled: usable,
    staleTime: 5 * 60_000,
    retry: obsRetry,
  });
}

export interface ReleaseComparisonProps {
  /** The telemetry scope on screen; the comparison narrows it to the promotion's environment. */
  scope: ObsScope;
  subject: string;
  target: CompareRef;
  /** The promotion's environment by name, for the heading. */
  envName?: string;
  width: HealthWindow;
  /**
   * The view the reader came from, for the way back. Absent when the
   * comparison IS the page's content (a release's Health tab): there is no
   * view underneath to go back to.
   */
  back?: { label: string; onBack: () => void };
  navigate: ObsNavigate;
  /** Where an issue the release opened links to. Absent: the Errors view of the panel this sits in (`navigate`). */
  issueHref?: (issueId: string) => string;
}

export function ReleaseComparison({ scope, subject, target, envName, width, back: way, navigate, issueHref }: ReleaseComparisonProps) {
  const delivery = useMemo(() => ({ orgId: scope.orgId, projectId: scope.projectId, envIds: [target.envId] }), [scope.orgId, scope.projectId, target.envId]);
  const health = useReleaseHealth(delivery, target.promotionId, HEALTH_WINDOW_MS[width]);
  // The environment filter is the promotion's; any other filter (a service, a route) still narrows the resources.
  const telemetry = useMemo<ObsScope>(
    () => ({ ...scope, envIds: [target.envId], filters: scope.filters.filter((f) => f.key !== "deployment.environment.name" && f.key !== "reliant.env.id") }),
    [scope, target.envId]
  );
  const resBefore = useResources(telemetry, "before", health.data?.before ?? null);
  const resAfter = useResources(telemetry, "after", health.data?.after ?? null);

  const back = way && (
    <Button variant="ghost" size="sm" leftIcon={<ArrowLeft className="h-3.5 w-3.5" />} onClick={way.onBack} data-testid="obs-compare-back">
      Back to {way.label}
    </Button>
  );
  const frame = (state: string, body: ReactNode) => (
    <div className="space-y-3" data-testid="obs-compare" data-state={state}>
      {back}
      {health.data && <ObsAsOf updatedAt={health.dataUpdatedAt} />}
      {body}
    </div>
  );

  if (health.isLoading) return frame("loading", <ObsLoadingRows testId="obs-compare-loading" rows={4} />);
  if ((health.error && !keepsLastResult(health.error, health.data)) || !health.data) {
    const kind = releaseHealthErrorKind(health.error);
    if (kind === "not-available") {
      return frame(
        "unavailable",
        <p role="status" className="rounded-lg border border-dashed border-border bg-surface-sunken px-4 py-5 text-sm text-ink-muted" data-testid="obs-compare-unavailable">
          Release health isn&apos;t available on this control plane, so there is no before-and-after verdict for this promotion. Its logs, traces and
          alerts are still in their own views.
        </p>
      );
    }
    if (kind === "not-found") {
      return frame(
        "not-found",
        <p role="status" className="rounded-lg border border-dashed border-border bg-surface-sunken px-4 py-5 text-sm text-ink-muted" data-testid="obs-compare-not-found">
          That promotion isn&apos;t in the release ledger any more, or isn&apos;t one you can see from here. Pick a release from the list to compare it.
        </p>
      );
    }
    return frame("error", <ObsError error={health.error} subject={subject} onRetry={() => void health.refetch()} retrying={health.isFetching} />);
  }

  const h = health.data;
  const openIssue = (id: string) => navigate({ view: "errors", issue: id, compare: undefined, cmpWindow: undefined });
  return (
    <div className="space-y-4" data-testid="obs-compare" data-state="ready" data-window={width} data-verdict={h.verdict}>
      {back}
      <ComparisonHeader health={h} target={target} envName={envName} />
      <div className="flex flex-wrap items-center gap-3">
        <div role="group" aria-label="Window on each side" className="inline-flex rounded-lg border border-border bg-surface-sunken p-0.5">
          {HEALTH_WINDOWS.map((w) => (
            <button
              key={w}
              type="button"
              aria-pressed={w === width}
              onClick={() => navigate({ cmpWindow: w === "15m" ? undefined : w }, { replace: true })}
              data-testid={`obs-compare-window-${w}`}
              className={cn(
                "rounded-md px-2.5 py-1 text-xs transition-colors focus:outline-none focus-visible:ring-2 focus-visible:ring-ring",
                w === width ? "bg-surface font-medium text-ink shadow-sm" : "text-ink-muted hover:text-ink"
              )}
            >
              {w}
            </button>
          ))}
        </div>
        <WindowsLine health={h} width={width} />
      </div>

      <Verdict health={h} />

      <Card padding="none" data-testid="obs-compare-services">
        <SectionTitle title="Requests, errors and latency" note="Server spans per service, weighted for sampling, before and after the promotion." />
        {h.services.length === 0 ? (
          <p className="px-4 py-5 text-sm text-ink-muted" data-testid="obs-compare-services-empty">
            No server traffic in either window.
          </p>
        ) : (
          <ServiceTable services={h.services} before={h.before} after={h.after} version={h.promotion?.version ?? ""} />
        )}
      </Card>

      <div className="grid gap-4 lg:grid-cols-2">
        <Card padding="none" data-testid="obs-compare-issues">
          <SectionTitle title="Issues this release opened" note="First seen in it, or regressed by it." />
          <IssueList
            newIssues={h.newIssues}
            regressed={h.regressedIssues}
            unchecked={h.notEvaluated.includes("new_issue") || h.notEvaluated.includes("regressed_issue")}
            issueHref={issueHref}
            onOpen={openIssue}
          />
        </Card>
        <Card padding="none" data-testid="obs-compare-alerts">
          <SectionTitle title="Alerts and restarts" note="Alerts opened since the promotion and still firing; restarts in the after window." />
          <AlertsAndRestarts health={h} />
        </Card>
      </div>

      <Card padding="none" data-testid="obs-compare-resources">
        <SectionTitle title="Resource usage" note="Average CPU and working-set memory per container, per workload, over the same windows." />
        {resBefore.isLoading || resAfter.isLoading ? (
          <div className="p-4">
            <ObsLoadingRows rows={2} testId="obs-compare-resources-loading" />
          </div>
        ) : (resBefore.error && !keepsLastResult(resBefore.error, resBefore.data)) || (resAfter.error && !keepsLastResult(resAfter.error, resAfter.data)) ? (
          <div className="p-4">
            <ObsError error={resBefore.error ?? resAfter.error} subject={subject} />
          </div>
        ) : (
          <ResourceRows before={resBefore.data} after={resAfter.data} />
        )}
      </Card>

      <Card padding="none" data-testid="obs-compare-thresholds">
        <SectionTitle title="Thresholds" note="What the findings were judged against, and where each value came from." />
        <Thresholds health={h} />
      </Card>
    </div>
  );
}

function ComparisonHeader({ health, target, envName }: { health: ReleaseHealth; target: CompareRef; envName?: string }) {
  const p = health.promotion;
  const version = p?.version || target.promotionId.slice(0, 12);
  const at = p?.promotedAtMs || target.promotedAtMs;
  return (
    <div className="space-y-1" data-testid="obs-compare-header">
      <h3 className="flex flex-wrap items-center gap-2 text-base font-semibold text-ink">
        Before and after <span className="font-mono">{version}</span>
        {envName && <> in <span className="font-mono">{envName}</span></>}
      </h3>
      <p className="flex flex-wrap items-center gap-x-2 gap-y-1 text-xs text-ink-muted">
        <span title={formatWhen(at)}>
          Promoted {formatWhen(at)} ({formatAgo(at)})
        </span>
        {p && (
          <span className="inline-flex items-center gap-1 font-mono" title={p.gitCommit || "No commit recorded"}>
            · <GitCommitHorizontal className="h-3.5 w-3.5" aria-hidden="true" />
            {p.gitCommit ? shortCommit(p.gitCommit) : "no commit recorded"}
          </span>
        )}
        {p?.previousVersion && (
          <span data-testid="obs-compare-replaced">
            · replaced <span className="font-mono text-ink">{p.previousVersion}</span>
          </span>
        )}
      </p>
    </div>
  );
}

function WindowsLine({ health, width }: { health: ReleaseHealth; width: HealthWindow }) {
  const { before, after } = health;
  if (!before || !after) return null;
  const asked = HEALTH_WINDOW_MS[width];
  const beforeLen = before.endMs - before.startMs;
  const afterLen = after.endMs - after.startMs;
  const shortened = Math.min(beforeLen, afterLen) < asked - 1000;
  return (
    <div className="text-xs text-ink-muted" data-testid="obs-compare-windows" data-after-complete={health.afterComplete}>
      <p>
        <span className="font-medium text-ink">Before</span> {formatWhen(before.startMs)} – {formatWhen(before.endMs)} ·{" "}
        <span className="font-medium text-ink">After</span> {formatWhen(after.startMs)} – {formatWhen(after.endMs)}
      </p>
      {!health.afterComplete ? (
        <p data-testid="obs-compare-open">
          The after window is still open ({formatSpan(afterLen)} of {formatSpan(asked)} so far); the verdict is re-read every minute until it closes.
        </p>
      ) : shortened ? (
        <p data-testid="obs-compare-clamped">A neighbouring promotion cut a side short, so each holds one release&apos;s traffic only.</p>
      ) : null}
    </div>
  );
}

const VERDICT: Record<ReleaseVerdict, { title: string; tone: string; icon: ReactNode }> = {
  pass: { title: "No regression found", tone: "text-success-ink", icon: <CheckCircle2 className="h-5 w-5" aria-hidden="true" /> },
  fail: { title: "This release made things worse", tone: "text-danger-ink", icon: <XCircle className="h-5 w-5" aria-hidden="true" /> },
  inconclusive: { title: "Not proven either way", tone: "text-warning-ink", icon: <CircleHelp className="h-5 w-5" aria-hidden="true" /> },
  unknown: { title: "No verdict", tone: "text-ink-muted", icon: <CircleHelp className="h-5 w-5" aria-hidden="true" /> },
};

/** The finding codes as a reader names them (not_evaluated lists the same tokens). */
export const FINDING_LABEL: Record<ReleaseFindingCode, string> = {
  error_rate: "Error rate",
  latency: "Latency",
  new_issue: "New issues",
  regressed_issue: "Regressed issues",
  alert_firing: "Firing alerts",
  workload_restarts: "Workload restarts",
};

export function findingLabel(code: string): string {
  return (FINDING_LABEL as Record<string, string>)[code] ?? code.replace(/_/g, " ");
}

/** A finding's measured value against its limit, in the unit its code implies. */
export function findingFigure(f: ReleaseFinding): string {
  switch (f.code) {
    case "error_rate":
      return `${formatPercent(f.value)} vs ${formatPercent(f.threshold)}`;
    case "latency":
      return `${f.value.toFixed(2)}× vs ${f.threshold.toFixed(2)}×`;
    default:
      return `${formatCount(f.value)} vs ${formatCount(f.threshold)}`;
  }
}

function inconclusiveWhy(h: ReleaseHealth): string {
  if (!h.afterComplete) return "The after window hasn't finished yet.";
  return "Too little traffic on either side to judge (every service is below the minimum request count).";
}

function Verdict({ health: h }: { health: ReleaseHealth }) {
  const v = VERDICT[h.verdict];
  const fails = h.findings.filter((f) => f.policy === "fail");
  const warns = h.findings.filter((f) => f.policy !== "fail");
  return (
    <Card padding="none" data-testid="obs-compare-verdict" data-verdict={h.verdict}>
      <div className="flex items-start gap-3 px-4 py-3">
        <span className={cn("mt-0.5 shrink-0", v.tone)}>{v.icon}</span>
        <div className="min-w-0 space-y-1">
          <p className={cn("text-sm font-semibold", v.tone)} data-testid="obs-compare-verdict-title">
            {v.title}
          </p>
          <p className="text-xs text-ink-muted">
            {h.verdict === "pass"
              ? "Every check the server ran stayed inside its threshold."
              : h.verdict === "fail"
                ? `${fails.length} finding${fails.length === 1 ? "" : "s"} crossed a threshold whose policy is fail.`
                : h.verdict === "inconclusive"
                  ? inconclusiveWhy(h)
                  : "The server returned no verdict for this promotion."}
          </p>
        </div>
      </div>
      {h.findings.length > 0 && (
        <ul className="divide-y divide-border/50 border-t border-border/60" data-testid="obs-compare-findings">
          {[...fails, ...warns].map((f, index) => (
            <li key={`${f.code}-${f.service}-${index}`} className="flex flex-wrap items-baseline gap-x-3 gap-y-0.5 px-4 py-2 text-xs" data-testid="obs-compare-finding" data-policy={f.policy} data-code={f.code ?? ""}>
              <span className={cn("inline-flex items-center gap-1 font-medium", f.policy === "fail" ? "text-danger-ink" : "text-warning-ink")}>
                <AlertTriangle className="h-3.5 w-3.5" aria-hidden="true" />
                {f.policy === "fail" ? "Fail" : "Warn"}
              </span>
              <span className="font-medium text-ink">{f.code ? FINDING_LABEL[f.code] : "Finding"}</span>
              {f.service && <span className="font-mono text-ink">{f.service}</span>}
              <span className="tabular-nums text-ink-muted">{findingFigure(f)}</span>
              {f.message && <span className="basis-full text-ink-muted">{f.message}</span>}
            </li>
          ))}
        </ul>
      )}
      {h.notEvaluated.length > 0 && (
        <p className="border-t border-border/60 px-4 py-2 text-xs text-ink-muted" data-testid="obs-compare-not-evaluated">
          <span className="font-medium text-ink">Not checked:</span> {h.notEvaluated.map(findingLabel).join(", ")}. This control plane can&apos;t read
          them, so {h.verdict === "pass" ? "the pass doesn't cover them" : "they are not part of this verdict"}.
        </p>
      )}
    </Card>
  );
}

function SectionTitle({ title, note }: { title: string; note: string }) {
  return (
    <div className="border-b border-border/60 px-4 py-3">
      <h4 className="text-sm font-medium text-ink">{title}</h4>
      <p className="text-xs text-ink-muted">{note}</p>
    </div>
  );
}

const MOVE_TONE: Record<Movement, string> = {
  "up-bad": "text-danger-ink",
  "down-bad": "text-danger-ink",
  "up-good": "text-success-ink",
  "down-good": "text-success-ink",
  flat: "text-ink-muted",
  none: "text-ink-muted",
};

const MOVE_WORD: Record<Movement, string> = {
  "up-bad": "worse",
  "down-bad": "worse",
  "up-good": "better",
  "down-good": "better",
  flat: "about the same",
  none: "",
};

function Change({
  before,
  after,
  format,
  higherIsWorse,
  kind = "relative",
  testId,
}: {
  before: number | null;
  after: number | null;
  format: (v: number) => string;
  higherIsWorse: boolean | null;
  kind?: "relative" | "points";
  testId?: string;
}) {
  const move = movement(before, after, higherIsWorse, kind === "points" ? 0.25 : 0.1);
  const text = changeText(before, after, kind);
  return (
    <span className="inline-flex flex-wrap items-baseline gap-x-1.5 tabular-nums" data-testid={testId} data-movement={move}>
      <span className="text-ink-muted">{before == null ? "—" : format(before)}</span>
      <ArrowRight className="h-3 w-3 self-center text-ink-muted" aria-label="to" />
      <span className="font-medium text-ink">{after == null ? "—" : format(after)}</span>
      {text && (
        <span className={cn("text-2xs font-medium", MOVE_TONE[move])} title={MOVE_WORD[move]}>
          {text}
          {higherIsWorse !== null && MOVE_WORD[move] && move !== "flat" ? <span className="sr-only"> ({MOVE_WORD[move]})</span> : null}
        </span>
      )}
    </span>
  );
}

/** Requests per second over a window the server reported. */
function rateOf(requests: number | undefined, window: TimeWindow | null): number | null {
  if (requests == null || !window) return null;
  const seconds = (window.endMs - window.startMs) / 1000;
  return seconds > 0 ? requests / seconds : null;
}

type ServiceRow = { service: ServiceHealthDelta };

function ServiceTable({ services, before, after, version }: { services: ServiceHealthDelta[]; before: TimeWindow | null; after: TimeWindow | null; version: string }) {
  const rows = [...services].sort((a, b) => (b.after?.requests ?? b.before?.requests ?? 0) - (a.after?.requests ?? a.before?.requests ?? 0) || a.service.localeCompare(b.service));
  const columns: DataTableColumn<ServiceRow>[] = [
    { key: "service", header: "Service", render: (_, { service: s }) => <span className="font-mono text-xs font-medium">{s.service || "(unnamed)"}</span> },
    {
      key: "requests",
      header: "Requests",
      render: (_, { service: s }) => (
        <span className="text-xs">
          <Change before={rateOf(s.before?.requests, before)} after={rateOf(s.after?.requests, after)} format={formatRate} higherIsWorse={null} />
        </span>
      ),
    },
    {
      key: "errors",
      header: "Error rate",
      render: (_, { service: s }) => (
        <span className="text-xs">
          <Change before={s.before?.errorRate ?? null} after={s.after?.errorRate ?? null} format={formatPercent} higherIsWorse kind="points" testId={`obs-compare-errors-${s.service}`} />
        </span>
      ),
    },
    {
      key: "p95",
      header: "p95 latency",
      render: (_, { service: s }) => (
        <span className="text-xs">
          <Change before={s.before?.p95Ms ?? null} after={s.after?.p95Ms ?? null} format={formatDuration} higherIsWorse testId={`obs-compare-p95-${s.service}`} />
        </span>
      ),
    },
    {
      key: "p99",
      header: "p99 latency",
      render: (_, { service: s }) => (
        <span className="text-xs">
          <Change before={s.before?.p99Ms ?? null} after={s.after?.p99Ms ?? null} format={formatDuration} higherIsWorse />
        </span>
      ),
    },
    {
      key: "share",
      header: version ? `Served by ${version}` : "Rollout",
      render: (_, { service: s }) => (
        <span className="text-xs tabular-nums" data-testid={`obs-compare-share-${s.service}`}>
          {formatPercent(s.versionShare)}
        </span>
      ),
    },
  ];
  return (
    <DataTable<ServiceRow>
      flush
      stacked
      ariaLabel="Each service before and after the promotion"
      columns={columns}
      data={rows.map((service) => ({ service }))}
      getRowKey={({ service }) => service.service}
      getRowProps={({ service }) => ({ className: "align-top", "data-testid": `obs-compare-service-${service.service}` })}
    />
  );
}

function IssueList({
  newIssues,
  regressed,
  unchecked,
  issueHref,
  onOpen,
}: {
  newIssues: Issue[];
  regressed: Issue[];
  unchecked: boolean;
  issueHref?: (id: string) => string;
  onOpen: (id: string) => void;
}) {
  const all = [...regressed.map((issue) => ({ issue, kind: "regressed" as const })), ...newIssues.map((issue) => ({ issue, kind: "new" as const }))];
  if (all.length === 0) {
    return (
      <p className="px-4 py-4 text-sm text-ink-muted" data-testid="obs-compare-issues-empty">
        {unchecked ? "Not checked: this control plane can't read issues." : "None: nothing new and nothing regressed."}
      </p>
    );
  }
  const linkClass = "block truncate text-left text-sm font-medium text-ink underline-offset-2 hover:underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring";
  return (
    <ul className="divide-y divide-border/50">
      {all.map(({ issue, kind }) => (
        <li key={issue.id} className="flex items-start gap-2 px-4 py-2" data-testid={`obs-compare-issue-${issue.id}`} data-kind={kind}>
          <StatusBadge status={kind === "regressed" ? "regressed" : "new"} />
          <div className="min-w-0 flex-1">
            {issueHref ? (
              <Link href={issueHref(issue.id)} className={linkClass} data-testid={`obs-compare-issue-open-${issue.id}`}>
                {issue.title || issue.fingerprint}
              </Link>
            ) : (
              <button type="button" onClick={() => onOpen(issue.id)} className={linkClass} data-testid={`obs-compare-issue-open-${issue.id}`}>
                {issue.title || issue.fingerprint}
              </button>
            )}
            <p className="flex flex-wrap gap-x-2 text-xs text-ink-muted">
              <span className="font-mono">{issue.service}</span>
              <span className="tabular-nums">{formatCount(issue.events)} events</span>
            </p>
          </div>
        </li>
      ))}
    </ul>
  );
}

function AlertsAndRestarts({ health: h }: { health: ReleaseHealth }) {
  const alertsUnchecked = h.notEvaluated.includes("alert_firing");
  if (h.firingAlerts.length === 0 && h.workloadFindings.length === 0) {
    return (
      <p className="px-4 py-4 text-sm text-ink-muted" data-testid="obs-compare-alerts-empty">
        {alertsUnchecked ? "No restarts. Alerts were not checked: this control plane can't read alert state." : "Nothing firing, nothing restarted."}
      </p>
    );
  }
  return (
    <ul className="divide-y divide-border/50">
      {h.firingAlerts.map((a) => (
        <li key={a.fingerprint || a.rule} className="flex items-start gap-2 px-4 py-2 text-sm" data-testid={`obs-compare-alert-${a.rule}`}>
          <Bell className="mt-0.5 h-4 w-4 shrink-0 text-danger-ink" aria-hidden="true" />
          <div className="min-w-0">
            <p className="font-medium text-ink">{a.rule}</p>
            <p className="text-xs text-ink-muted">firing since {formatWhen(a.firingAtMs)}</p>
          </div>
        </li>
      ))}
      {h.workloadFindings.map((w) => (
        <li key={w.workload} className="flex items-start gap-2 px-4 py-2 text-sm" data-testid={`obs-compare-restarts-${w.workload}`}>
          <RotateCw className="mt-0.5 h-4 w-4 shrink-0 text-warning-ink" aria-hidden="true" />
          <div className="min-w-0">
            <p className="font-mono text-ink">{w.workload}</p>
            <p className="text-xs text-ink-muted">
              {formatCount(w.restarts)} restart{w.restarts === 1 ? "" : "s"}
              {w.lastTerminatedReason && ` · last: ${w.lastTerminatedReason}`}
            </p>
          </div>
        </li>
      ))}
    </ul>
  );
}

function ResourceRows({ before, after }: { before: PanelResult | undefined; after: PanelResult | undefined }) {
  const rows = compareWorkloads(before, after);
  if (rows.length === 0) {
    return (
      <p className="px-4 py-5 text-sm text-ink-muted" data-testid="obs-compare-resources-empty">
        No container metrics in either window: the environment&apos;s workloads report none, or none ran.
      </p>
    );
  }
  const columns: DataTableColumn<WorkloadRow>[] = [
    { key: "workload", header: "Workload", render: (_, { row: r }) => <span className="font-mono text-xs font-medium">{r.workload || "(unnamed)"}</span> },
    {
      key: "cpu",
      header: "CPU",
      render: (_, { row: r }) => (
        <span className="text-xs">
          <Change before={r.cpu[0]} after={r.cpu[1]} format={formatCores} higherIsWorse />
        </span>
      ),
    },
    {
      key: "memory",
      header: "Memory",
      render: (_, { row: r }) => (
        <span className="text-xs">
          <Change before={r.memory[0]} after={r.memory[1]} format={formatBytes} higherIsWorse testId={`obs-compare-memory-${r.workload}`} />
        </span>
      ),
    },
  ];
  return (
    <DataTable<WorkloadRow>
      flush
      stacked
      ariaLabel="Each workload's resource use before and after the promotion"
      columns={columns}
      data={rows.map((row) => ({ row }))}
      getRowKey={({ row }) => row.workload}
      getRowProps={({ row }) => ({ "data-testid": `obs-compare-workload-${row.workload}` })}
    />
  );
}

type WorkloadRow = { row: WorkloadComparison };

const SOURCE_LABEL: Record<string, string> = { release_shape: "declared in the release shape", default: "default", request: "this request" };

function Thresholds({ health: h }: { health: ReleaseHealth }) {
  if (h.thresholds.length === 0) return <p className="px-4 py-4 text-sm text-ink-muted">The server reported no thresholds.</p>;
  return (
    <table className="w-full text-xs">
      <caption className="sr-only">Thresholds applied, with their source</caption>
      <tbody>
        {h.thresholds.map((t) => (
          <tr key={t.name} className="border-b border-border/50 last:border-b-0" data-testid={`obs-compare-threshold-${t.name}`}>
            <th scope="row" className="px-4 py-1.5 text-left font-mono font-normal text-ink">
              {t.name}
            </th>
            <td className="px-4 py-1.5 font-mono tabular-nums text-ink">{t.value}</td>
            <td className="px-4 py-1.5 text-ink-muted">{SOURCE_LABEL[t.source] ?? t.source}</td>
          </tr>
        ))}
      </tbody>
    </table>
  );
}
