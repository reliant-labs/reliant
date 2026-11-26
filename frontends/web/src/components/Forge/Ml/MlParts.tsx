// Copyright (c) 2026 Reliant Labs

/**
 * What the ML pages share: the page frame (breadcrumbs, title, the "Early
 * access" label on Training, Evals, Models and Inference), the states every
 * read can land in (loading, no data yet, a failure, not entitled), and a
 * code block for KCL and commands. The chart, table and timeline kit is
 * ./kit.
 *
 * NOT ENTITLED IS NOT AN ERROR. A read answering FEATURE_NOT_ENTITLED (the
 * entitlement was revoked since the gate read it) reports it to the gate
 * (MlGateContext), and the whole route becomes the one access page.
 */

import { createContext, useContext, useEffect, type ReactNode } from "react";
import { ChevronRight, Cpu, RefreshCw } from "lucide-react";
import type { UseQueryResult } from "@tanstack/react-query";

import Button from "@/components/forge-ui/button";
import Card from "@/components/forge-ui/card";
import { FilterBar, FilterMenu } from "@/components/forge-ui/filter_bar";
import Tag from "@/components/forge-ui/tag";
import { mlErrorKind } from "@/services/controlPlane/ml/errors";
import Link from "@/components/forge-ui/link";
import type { MlOutcome } from "@/services/controlPlane/ml/types";
import { obsErrorMessage } from "@/services/controlPlane/observability/errors";

import { ML_EARLY_ACCESS_VIEWS, ML_VIEW_LABELS, orgPath, type MlView } from "../Console/paths";
import { ObsLoadingRows, StateFrame } from "../Observability/ObsStates";

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
  overview: "What is running, what is wasting money, and what is broken, across your clusters.",
  runs: "Experiments, runs and their phases: progress, goodput, cost and what went wrong.",
  evals: "Every candidate against production on the release-gate suite: deltas, significance and gates.",
  models: "Model versions by digest: where each came from, how it evaluates, and where it serves.",
  inference: "Serving SLOs, latency by stage, engine saturation, cost per token and each revision's verdict.",
  fleet: "Accelerators by pool and node: allocation against use, health from DCGM and node-problem-detector, and idle cost.",
  queues: "Kueue quotas and usage, what waits and why, preemptions, and where pod start time goes.",
  recommendations: "What to fix first, by money and GPU-hours at stake, with the evidence and the change that fixes it.",
  setup: "Per cluster: which telemetry and ML components are in place, adopted where they exist, installed only where missing.",
};

export interface MlCrumb {
  label: string;
  href?: string;
}

/**
 * One ML page: breadcrumbs (`ML › Training › llama-8b-support-v3 › sft`),
 * its header (the early-access label where it applies), then the body. A
 * drill-down passes its own `title`, `meta` and `crumbs`; a view's list page
 * passes none and gets its label and subtitle.
 */
export function MlFrame({
  view,
  title,
  subtitle,
  meta,
  crumbs,
  actions,
  controls,
  children,
  testId,
}: {
  view: MlView;
  title?: string;
  subtitle?: ReactNode;
  meta?: ReactNode;
  crumbs?: readonly MlCrumb[];
  actions?: ReactNode;
  controls?: ReactNode;
  children: ReactNode;
  testId?: string;
}) {
  const early = ML_EARLY_ACCESS_VIEWS.includes(view);
  const trail: MlCrumb[] = [{ label: "ML", href: orgPath({ kind: "ml", view: "overview" }) }, { label: ML_VIEW_LABELS[view], href: orgPath({ kind: "ml", view }) }, ...(crumbs ?? [])];
  return (
    <div className="mx-auto w-full min-w-0 max-w-screen-2xl space-y-4" data-testid={testId ?? `ml-page-${view}`}>
      <div className="space-y-1">
        <nav aria-label="Breadcrumb" className="flex flex-wrap items-center gap-1 text-meta text-ink-muted" data-testid="ml-crumbs">
          {trail.map((crumb, i) => (
            <span key={`${i}-${crumb.label}`} className="flex items-center gap-1">
              {i > 0 && <ChevronRight className="h-3 w-3" aria-hidden="true" />}
              {crumb.href && i < trail.length - 1 ? (
                <Link href={crumb.href} className="hover:text-ink hover:underline">
                  {crumb.label}
                </Link>
              ) : (
                <span className={i === trail.length - 1 ? "text-ink" : undefined}>{crumb.label}</span>
              )}
            </span>
          ))}
        </nav>
        <div className="flex flex-wrap items-start justify-between gap-3">
          <div className="min-w-0 space-y-0.5">
            <div className="flex flex-wrap items-center gap-2">
              <h1 className="text-title text-ink">{title ?? ML_VIEW_LABELS[view]}</h1>
              {early && <Tag title="Early access: what this page shows may change">Early access</Tag>}
              {meta}
            </div>
            {(subtitle ?? (title ? undefined : SUBTITLES[view])) && <p className="text-ui text-ink-muted">{subtitle ?? SUBTITLES[view]}</p>}
          </div>
          {actions && <div className="flex shrink-0 flex-wrap items-center gap-2">{actions}</div>}
        </div>
      </div>
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
