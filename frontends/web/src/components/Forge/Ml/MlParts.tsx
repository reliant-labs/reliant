// Copyright (c) 2026 Reliant Labs

/**
 * What the six ML pages share: the page frame (title, the "Early access"
 * label on Runs, Models and Inference), the states every read can land in
 * (loading, no data yet, a failure, not entitled), a code block for KCL and
 * commands, and a pod-start breakdown.
 *
 * NOT ENTITLED IS NOT AN ERROR. A read answering FEATURE_NOT_ENTITLED (the
 * entitlement was revoked since the gate read it) reports it to the gate
 * (MlGateContext), and the whole route becomes the one access page.
 */

import { createContext, useContext, useEffect, type ReactNode } from "react";
import { Cpu, RefreshCw } from "lucide-react";
import type { UseQueryResult } from "@tanstack/react-query";

import Button from "@/components/forge-ui/button";
import Card from "@/components/forge-ui/card";
import { FilterBar, FilterMenu } from "@/components/forge-ui/filter_bar";
import Tag from "@/components/forge-ui/tag";
import { mlErrorKind } from "@/services/controlPlane/ml/errors";
import type { MlOutcome, MlPodStartBreakdown } from "@/services/controlPlane/ml/types";
import { obsErrorMessage } from "@/services/controlPlane/observability/errors";

import { ML_EARLY_ACCESS_VIEWS, ML_VIEW_LABELS, type MlView } from "../Console/paths";
import { Header } from "../Console/consolePageFrame";
import { ObsLoadingRows, StateFrame } from "../Observability/ObsStates";
import { seconds } from "./mlModel";

/** What the gate hands its pages: the org they read, and how to say the org is not entitled after all. */
export interface MlGate {
  orgId: string;
  reportNotEntitled: () => void;
}

export const MlGateContext = createContext<MlGate | null>(null);

export function useMlGate(): MlGate {
  const gate = useContext(MlGateContext);
  if (!gate) throw new Error("useMlGate outside the ML gate (MlRoute)");
  return gate;
}

const SUBTITLES: Record<MlView, string> = {
  machines: "Accelerator nodes in your connected clusters, by pool: allocation against use, and health with its evidence.",
  stack: "The ML components found in each cluster: adopted where they exist, installed only where they are missing.",
  recommendations: "What to fix first, worst first, with the evidence and the change that fixes it.",
  runs: "Training runs, newest first.",
  models: "Model versions by digest: the run that produced each, its evals, and where it serves.",
  inference: "Inference services: serving latency, throughput, cold starts and each release's verdict.",
};

/** One ML page: its header (with the early-access label where it applies), then the body. */
export function MlFrame({ view, controls, children }: { view: MlView; controls?: ReactNode; children: ReactNode }) {
  const early = ML_EARLY_ACCESS_VIEWS.includes(view);
  return (
    <div className="mx-auto w-full min-w-0 max-w-[var(--page-max)] space-y-4" data-testid={`ml-page-${view}`}>
      <Header
        title={ML_VIEW_LABELS[view]}
        subtitle={SUBTITLES[view]}
        meta={early ? <Tag title="Early access: what this page shows may change">Early access</Tag> : undefined}
      />
      {controls}
      {children}
    </div>
  );
}

/** A titled panel: heading and an optional note on one line, then the body. */
export function Section({ title, note, testId, children, flush }: { title: ReactNode; note?: ReactNode; testId?: string; children: ReactNode; flush?: boolean }) {
  return (
    <Card padding="none" className="overflow-hidden" data-testid={testId}>
      <div className="flex flex-wrap items-baseline justify-between gap-x-3 gap-y-1 px-4 pb-2 pt-3">
        <h2 className="text-section text-ink">{title}</h2>
        {note && <p className="text-meta text-ink-muted">{note}</p>}
      </div>
      <div className={flush ? undefined : "px-4 pb-4"}>{children}</div>
    </Card>
  );
}

/** Entitled, and nothing to read yet: what fills the page, said plainly. Never invented rows. */
export function MlNoData({ what }: { what: string }) {
  return (
    <StateFrame testId="ml-no-data" icon={<Cpu className="h-4 w-4" />} title={`No ${what} yet`}>
      <p>
        ML data appears here once a cluster is connected with <code className="font-mono text-ink">reliant forge ml</code>. Nothing is shown until it
        reports.
      </p>
    </StateFrame>
  );
}

function MlReadError({ error, what, onRetry, retrying }: { error: unknown; what: string; onRetry: () => void; retrying: boolean }) {
  const kind = mlErrorKind(error);
  if (kind === "denied") {
    return (
      <StateFrame testId="ml-error-denied" tone="error" icon={<Cpu className="h-4 w-4" />} title={`You can't read this organization's ${what}`}>
        <p>Only members of the organization can. Switch to an organization you belong to.</p>
      </StateFrame>
    );
  }
  return (
    <StateFrame
      testId="ml-error"
      tone="error"
      icon={<Cpu className="h-4 w-4" />}
      title={`Couldn't read ${what}`}
      actions={
        <Button variant="outline" size="sm" leftIcon={<RefreshCw className="h-3.5 w-3.5" />} onClick={onRetry} disabled={retrying}>
          {retrying ? "Retrying…" : "Retry"}
        </Button>
      }
    >
      <p>The control plane did not answer.</p>
      <p className="break-words font-mono text-meta">{obsErrorMessage(error)}</p>
    </StateFrame>
  );
}

/** Tells the gate once that the org is not entitled; renders nothing meanwhile. */
function NotEntitled() {
  const { reportNotEntitled } = useMlGate();
  useEffect(() => reportNotEntitled(), [reportNotEntitled]);
  return null;
}

/**
 * One ML read, in its states: loading, a failure, not entitled (reported to
 * the gate), no data yet, or `children` with the data.
 */
export function MlRead<T>({ query, what, children }: { query: UseQueryResult<MlOutcome<T>, unknown>; what: string; children: (data: T) => ReactNode }) {
  if (query.isLoading) {
    return (
      <Card padding="none">
        <ObsLoadingRows testId="ml-loading" />
      </Card>
    );
  }
  if (query.error || !query.data) {
    return <MlReadError error={query.error} what={what} onRetry={() => void query.refetch()} retrying={query.isFetching} />;
  }
  const outcome = query.data;
  if (outcome.kind === "not-entitled") return <NotEntitled />;
  if (outcome.kind === "no-data") return <MlNoData what={what} />;
  return <>{children(outcome.data)}</>;
}

/**
 * The App filter of Runs, Models and Inference (`?app=`): the apps the rows
 * on screen name, and the one the URL names. Unset reads every app.
 */
export function AppFilter({ app, apps, onApp, testId }: { app: string; apps: readonly string[]; onApp: (app: string | undefined) => void; testId: string }) {
  const options = [...new Set([...apps.filter(Boolean), ...(app ? [app] : [])])].sort().map((name) => ({ value: name, label: name }));
  return (
    <FilterBar label="Filter by app" active={app ? 1 : 0} onClear={() => onApp(undefined)} testId={testId}>
      <FilterMenu label="App" value={app || undefined} options={options} onChange={onApp} testId={`${testId}-app`} />
    </FilterBar>
  );
}

/** KCL, a command or a declaration: monospace, in a well, selectable. */
export function CodeBlock({ code, label, testId }: { code: string; label: string; testId?: string }) {
  return (
    <figure className="min-w-0 space-y-1" data-testid={testId}>
      <figcaption className="text-meta font-medium text-ink-muted">{label}</figcaption>
      <pre className="max-h-80 overflow-auto whitespace-pre rounded-md border border-border/60 bg-surface-sunken px-3 py-2 font-mono text-code text-ink">{code}</pre>
    </figure>
  );
}

/**
 * Where a pod's start time goes: one bar split by each stage's p50 share, and
 * the stages' p50 and p95 in a table under it. The bar is one hue at stepped
 * opacity: the stages are one series' parts, not separate series.
 */
export function StageBreakdown({ breakdown, title = "Pod start", testId }: { breakdown: MlPodStartBreakdown; title?: string; testId?: string }) {
  const total = breakdown.stages.reduce((sum, stage) => sum + stage.p50Seconds, 0);
  // A data colour (web-styling: inline style is how data colours are painted).
  const alphas = [1, 0.8, 0.6, 0.45, 0.3, 0.2];
  const fill = (index: number) => ({ backgroundColor: `hsl(var(--chart-1) / ${alphas[index % alphas.length]})` });
  return (
    <div className="space-y-2" data-testid={testId}>
      <p className="text-meta text-ink-muted">
        {title}
        {breakdown.workload && (
          <>
            {" "}
            of <span className="font-mono text-ink">{breakdown.workload}</span>
          </>
        )}{" "}
        · {breakdown.sampleCount} {breakdown.sampleCount === 1 ? "start" : "starts"} · p50 {seconds(total)} in total
      </p>
      {total > 0 && (
        <div className="flex h-2.5 w-full overflow-hidden rounded-full bg-surface-sunken" role="img" aria-label={`${title}: ${breakdown.stages.map((s) => `${s.name} ${seconds(s.p50Seconds)}`).join(", ")}`}>
          {breakdown.stages.map((stage, index) => (
            <span
              key={stage.name}
              className={index > 0 ? "h-full border-l border-surface" : "h-full"}
              style={{ ...fill(index), width: `${(stage.p50Seconds / total) * 100}%` }}
              title={`${stage.name}: p50 ${seconds(stage.p50Seconds)}`}
            />
          ))}
        </div>
      )}
      <table className="w-full text-ui">
        <thead>
          <tr className="text-left text-meta text-ink-muted">
            <th className="py-1 font-medium">Stage</th>
            <th className="py-1 text-right font-medium">p50</th>
            <th className="py-1 text-right font-medium">p95</th>
          </tr>
        </thead>
        <tbody>
          {breakdown.stages.map((stage, index) => (
            <tr key={stage.name} className="border-t border-border-subtle">
              <td className="py-1 text-ink">
                <span className="mr-2 inline-block h-2 w-2 rounded-full" style={fill(index)} aria-hidden="true" />
                {stage.name}
              </td>
              <td className="py-1 text-right tabular-nums text-ink">{seconds(stage.p50Seconds)}</td>
              <td className="py-1 text-right tabular-nums text-ink-muted">{seconds(stage.p95Seconds)}</td>
            </tr>
          ))}
        </tbody>
      </table>
    </div>
  );
}
