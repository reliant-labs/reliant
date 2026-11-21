// Copyright (c) 2026 Reliant Labs

/**
 * /forge/p/:project/tests — Tests › Overview: one branch's test health, from
 * control-plane TestsService.GetTestSummary (forge
 * docs/proposals/tests-coverage-flakes.md §3.2).
 *
 * One headline, then one chart. The headline answers, left to right: how much
 * of the code the tests reach (combined coverage of the newest commit report,
 * and each lane behind it), whether the suite is green (the pass rate over
 * the last runs, with its n), what needs triage (flaky and quarantined
 * tests), and how fresh all of it is (the last run). The chart is coverage
 * over the branch's recent commits, combined and per lane, with the releases
 * cut from them marked.
 *
 * Honest about its data: a figure with nothing behind it is a dash with the
 * reason, never 0% or 100%; a run still being processed is "Processing";
 * "62%, unit only" says which lanes it merges. A control plane without the
 * service says so, and an empty record names the commands that fill it.
 *
 * The branch is `?branch=` (default: the project's default branch, which the
 * answer names); the environment is the top bar's scope.
 */

import { useMemo, type ReactNode } from "react";
import { useSearch } from "@tanstack/react-router";

import Card from "@/components/forge-ui/card";
import Link from "@/components/forge-ui/link";
import Status from "@/components/forge-ui/status";
import { useTestSummary } from "@/hooks/test-queries";
import type { TestSummary } from "@/services/controlPlane/tests/types";
import { cn } from "@/lib/utils";

import { useForgeConsole } from "../Console/consoleContext";
import { UPlotChart } from "../Observability/charts/UPlotChart";
import { formatAgo, formatWhen } from "../Observability/format";
import { ObsLoadingRows } from "../Observability/ObsStates";
import { formatDelta, formatPassRate, formatPercent, headlineCoverage, runSourceWords, runVerdict, shortSha, trendChart } from "./testsModel";
import { TestsFrame, useTestsScope, type TestsScope } from "./testsScope";
import { TestsError, TestsNoRunsYet } from "./TestsStates";

const SUBTITLE = "Coverage, pass rate and flaky tests of the project's own test suite, per commit.";

export function TestsOverviewPage() {
  const { href, projectId } = useForgeConsole();
  const resolved = useTestsScope("test runs");
  const search = useSearch({ strict: false }) as { branch?: string };
  return (
    <TestsFrame title="Tests" subtitle={SUBTITLE} testId="console-tests">
      {"blocked" in resolved ? (
        resolved.blocked
      ) : (
        <TestsOverviewView
          scope={resolved.scope}
          branch={search.branch}
          projectId={projectId}
          hrefs={{
            flaky: href({ kind: "tests-flaky" }),
            runs: href({ kind: "tests-runs" }),
            run: (runId) => href({ kind: "tests-run", run: runId }),
            coverage: href({ kind: "tests-coverage" }),
          }}
        />
      )}
    </TestsFrame>
  );
}

export interface TestsOverviewHrefs {
  flaky: string;
  runs: string;
  run: (runId: string) => string;
  coverage: string;
}

export interface TestsOverviewViewProps {
  scope: TestsScope;
  /** Empty: the project's default branch. */
  branch?: string;
  projectId?: string | null;
  hrefs: TestsOverviewHrefs;
  /** "Now", for relative times (tests pin it). */
  now?: number;
}

/** The page below its header, free of the console: what tests and the preview render. */
export function TestsOverviewView({ scope, branch, projectId, hrefs, now }: TestsOverviewViewProps) {
  const summary = useTestSummary(scope.project, { branch, environmentId: scope.environmentId });

  if (summary.isLoading) {
    return (
      <Card padding="none">
        <ObsLoadingRows testId="tests-loading" />
      </Card>
    );
  }
  if (summary.error || !summary.data) {
    return <TestsError error={summary.error} subject="this project's tests" onRetry={() => void summary.refetch()} retrying={summary.isFetching} />;
  }
  const data = summary.data;
  if (!data.latest && !data.lastRun) return <TestsNoRunsYet projectId={projectId} branch={branch} />;
  return <Overview summary={data} hrefs={hrefs} now={now ?? Date.now()} />;
}

function Overview({ summary, hrefs, now }: { summary: TestSummary; hrefs: TestsOverviewHrefs; now: number }) {
  return (
    <div className="space-y-4" data-testid="tests-overview">
      <p className="text-meta text-ink-muted" data-testid="tests-branch">
        Branch <code className="font-mono text-ink">{summary.branch}</code>
        {summary.latest && (
          <>
            {" "}
            · newest report <code className="font-mono text-ink">{shortSha(summary.latest.gitCommit)}</code>
          </>
        )}
      </p>
      <Headline summary={summary} hrefs={hrefs} now={now} />
      <CoverageTrend summary={summary} />
    </div>
  );
}

function Cell({ label, figure, children, testId, primary }: { label: string; figure: ReactNode; children?: ReactNode; testId: string; primary?: boolean }) {
  return (
    <div className="min-w-0 space-y-1 p-4" data-testid={testId}>
      <p className="text-meta font-medium text-ink-muted">{label}</p>
      <div className={cn("tabular-nums text-ink", primary ? "text-figure" : "text-2xl font-semibold")}>{figure}</div>
      {children && <div className="space-y-0.5 text-meta text-ink-muted">{children}</div>}
    </div>
  );
}

const linkClass = "font-medium text-ink underline-offset-2 hover:underline";

function Headline({ summary, hrefs, now }: { summary: TestSummary; hrefs: TestsOverviewHrefs; now: number }) {
  const coverage = headlineCoverage(summary.latest);
  const run = summary.lastRun;
  const verdict = run ? runVerdict(run) : null;
  const ranMs = run ? (run.finishedMs ?? run.createdMs) : null;
  return (
    <Card padding="none" data-testid="tests-headline">
      <div className="grid grid-cols-1 divide-y divide-border-subtle sm:grid-cols-2 sm:divide-y-0 lg:grid-cols-4 lg:divide-x">
        <Cell label="Coverage" testId="tests-headline-coverage" primary figure={coverage ? formatPercent(coverage.percent) : "—"}>
          {coverage ? (
            <>
              <p>
                {coverage.delta !== null && (
                  <span className={cn("mr-1 font-medium", coverage.delta < 0 ? "text-danger-ink" : "text-ink")} data-testid="tests-coverage-delta">
                    {formatDelta(coverage.delta)}
                  </span>
                )}
                {coverage.delta !== null ? "against the base report" : "no base report to compare"}
              </p>
              <p data-testid="tests-coverage-lanes">
                {coverage.lanes.length === 1
                  ? `${coverage.lanes[0]!.lane} only so far`
                  : coverage.lanes.map(({ lane, percent }) => `${lane} ${percent.toFixed(1)}`).join(" · ")}
              </p>
              <Link href={hrefs.coverage} className={linkClass}>
                Browse coverage
              </Link>
            </>
          ) : (
            <p>{summary.latest ? "The newest report has no coverage." : "No report yet: its runs are still being processed."}</p>
          )}
        </Cell>
        <Cell label="Pass rate" testId="tests-headline-pass-rate" figure={summary.passRate === null ? "—" : formatPassRate(summary.passRate)}>
          <p>{summary.passRateRuns > 0 ? `over the last ${summary.passRateRuns} ${summary.passRateRuns === 1 ? "run" : "runs"}` : "No run judged yet"}</p>
          <Link href={hrefs.runs} className={linkClass}>
            See runs
          </Link>
        </Cell>
        <Cell label="Flaky tests" testId="tests-headline-flaky" figure={summary.flaky}>
          <p>
            {summary.quarantined} quarantined · {summary.suspected} suspected
          </p>
          {summary.hostFailures30d > 0 && <p>{summary.hostFailures30d} host failures in 30 days, not counted</p>}
          <Link href={hrefs.flaky} className={linkClass}>
            Triage
          </Link>
        </Cell>
        <Cell
          label="Last run"
          testId="tests-headline-last-run"
          figure={ranMs !== null ? <span title={formatWhen(ranMs, now)}>{formatAgo(ranMs, now)}</span> : "—"}
        >
          {run && verdict ? (
            <>
              <p className="flex items-center gap-1.5">
                <Status tone={verdict.tone} label={verdict.words} size="sm" />
              </p>
              <p>
                {runSourceWords(run)} · {run.lane} · {run.branch || "no branch"} <code className="font-mono">{shortSha(run.gitCommit)}</code>
              </p>
              <Link href={hrefs.run(run.runId)} className={linkClass}>
                Open run
              </Link>
            </>
          ) : (
            <p>No run recorded</p>
          )}
        </Cell>
      </div>
    </Card>
  );
}

function CoverageTrend({ summary }: { summary: TestSummary }) {
  const chart = useMemo(() => trendChart(summary), [summary]);
  return (
    <Card padding="none" data-testid="tests-trend">
      <div className="flex flex-wrap items-baseline justify-between gap-2 px-4 pt-3">
        <h2 className="text-section text-ink">Coverage over time</h2>
        <p className="text-meta text-ink-muted">
          {chart.timestamps.length} {chart.timestamps.length === 1 ? "commit" : "commits"} on {summary.branch}
          {chart.markers.length > 0 && ` · ${chart.markers.length} ${chart.markers.length === 1 ? "release" : "releases"} marked`}
        </p>
      </div>
      <div className="px-2 pb-2 pt-1">
        {chart.timestamps.length < 2 ? (
          <p className="px-2 py-12 text-center text-meta text-ink-muted" data-testid="tests-trend-short">
            The trend starts with the second commit that records coverage.
          </p>
        ) : (
          <UPlotChart
            height={220}
            timestamps={chart.timestamps}
            series={chart.series}
            markers={chart.markers}
            unit="percent"
            label={`Coverage of ${summary.branch} over its last ${chart.timestamps.length} commits`}
          />
        )}
      </div>
    </Card>
  );
}
