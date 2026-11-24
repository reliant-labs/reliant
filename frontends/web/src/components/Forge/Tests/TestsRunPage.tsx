// Copyright (c) 2026 Reliant Labs

/**
 * /forge/p/:project/tests/runs/:run — one recorded run (control-plane
 * TestsService.GetTestRun; forge docs/proposals/tests-coverage-flakes.md
 * §3.2).
 *
 * Top to bottom: what the run was (verdict, lane, source, commit and branch,
 * CI run, when and how long) and its totals; then what needs reading — its
 * failures, each with its output (monospace, collapsed but the first; the
 * server has already scrubbed and truncated it) and how ingest classified it;
 * the tests that passed only on a retry (a flake signal); the slowest tests
 * (the `test-budget` view); and the packages it ran that recorded no test
 * (the `verify-test-run` rule).
 *
 * A host or correlated failure is said to be one: it never counts against
 * its test. A run still being ingested says so and shows no counts.
 */

import type { ReactNode } from "react";
import { Code, ConnectError } from "@connectrpc/connect";
import { useParams } from "@tanstack/react-router";

import Card from "@/components/forge-ui/card";
import Link from "@/components/forge-ui/link";
import Status from "@/components/forge-ui/status";
import Tag from "@/components/forge-ui/tag";
import { useTestRun } from "@/hooks/test-queries";
import type { TestFailure, TestRun, TestRunDetail } from "@/services/controlPlane/tests/types";
import { cn } from "@/lib/utils";

import { useForgeConsole } from "../Console/consoleContext";
import { formatAgo, formatDuration, formatWhen } from "../Observability/format";
import { ObsLoadingRows, StateFrame } from "../Observability/ObsStates";
import { failurePlace, failureVerdictWords, runAtMs, runDurationMs, runSourceLabel } from "./runsModel";
import { formatPercent, runVerdict, shortSha } from "./testsModel";
import { FailureOutput, OutLink, Section, TestName } from "./TestsParts";
import { TestsFrame } from "./testsScope";
import { TestsError } from "./TestsStates";

export function TestsRunPage() {
  const { forgeProject, href } = useForgeConsole();
  const { run } = useParams({ strict: false }) as { run?: string };
  const runId = run ?? "";
  return (
    <TestsFrame title="Run" subtitle={runId ? <code className="break-all font-mono">{runId}</code> : undefined} testId="console-tests-run">
      <TestsRunView
        project={forgeProject}
        runId={runId}
        hrefs={{
          runs: (search) => href({ kind: "tests-runs" }, search),
          test: (testId) => href({ kind: "tests-flaky-test", test: testId }),
        }}
      />
    </TestsFrame>
  );
}

export interface TestsRunHrefs {
  /** Tests › Runs, filtered (`{ commit }`, `{ branch }`). */
  runs: (search?: Record<string, string>) => string;
  /** A test's page (its outcomes, flake record and signatures). */
  test: (testId: string) => string;
}

export interface TestsRunViewProps {
  project: string;
  runId: string;
  hrefs: TestsRunHrefs;
  now?: number;
}

/** The page below its header, free of the console: what tests and the preview render. */
export function TestsRunView({ project, runId, hrefs, now }: TestsRunViewProps) {
  const read = useTestRun(project, runId);
  if (read.isLoading) {
    return (
      <Card padding="none">
        <ObsLoadingRows testId="tests-run-loading" />
      </Card>
    );
  }
  if (read.error || !read.data?.run) {
    return (
      <TestsError
        error={read.error ?? notFound()}
        subject={runId ? `Run ${runId}` : "This run"}
        onRetry={() => void read.refetch()}
        retrying={read.isFetching}
        testId="tests-run-error"
      />
    );
  }
  return <RunDetail detail={read.data} run={read.data.run} hrefs={hrefs} now={now ?? Date.now()} />;
}

/** An answer with no run in it is a run this project does not have. */
const notFound = () => new ConnectError("the answer named no run", Code.NotFound);

function RunDetail({ detail, run, hrefs, now }: { detail: TestRunDetail; run: TestRun; hrefs: TestsRunHrefs; now: number }) {
  return (
    <div className="space-y-4" data-testid="tests-run">
      <RunFacts run={run} hrefs={hrefs} now={now} packageCount={detail.packageCount} />
      {run.ingestState === "pending" ? (
        <StateFrame testId="tests-run-processing" icon={null} title="This run is still being processed">
          <p>Its counts, failures and timings appear here when ingest finishes, usually within a minute of the upload.</p>
        </StateFrame>
      ) : run.ingestState === "failed" ? (
        <StateFrame testId="tests-run-unreadable" tone="error" icon={null} title="This run couldn't be read">
          <p>The upload was kept, but its results could not be parsed, so it has no counts and decides nothing.</p>
          {run.ingestError && <p className="break-words font-mono text-meta">{run.ingestError}</p>}
        </StateFrame>
      ) : (
        <>
          <Failures failures={detail.failures} failed={run.totals.failed} hrefs={hrefs} />
          <div className="grid gap-4 lg:grid-cols-2">
            <RetryPasses detail={detail} hrefs={hrefs} />
            <Slowest detail={detail} hrefs={hrefs} />
          </div>
          <ZeroEvidence detail={detail} />
        </>
      )}
    </div>
  );
}

function Fact({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="min-w-0">
      <dt className="text-meta font-medium text-ink-muted">{label}</dt>
      <dd className="mt-0.5 min-w-0 text-ink">{children}</dd>
    </div>
  );
}

function Figure({ label, value, testId, tone }: { label: string; value: ReactNode; testId: string; tone?: "danger" | "warning" }) {
  return (
    <div className="min-w-0 p-4" data-testid={testId}>
      <p className="text-meta font-medium text-ink-muted">{label}</p>
      <p className={cn("text-2xl font-semibold tabular-nums", tone === "danger" ? "text-danger-ink" : tone === "warning" ? "text-warning-ink" : "text-ink")}>{value}</p>
    </div>
  );
}

const linkClass = "text-ink underline-offset-2 hover:underline";

function RunFacts({ run, hrefs, now, packageCount }: { run: TestRun; hrefs: TestsRunHrefs; now: number; packageCount: number }) {
  const verdict = runVerdict(run);
  const took = runDurationMs(run);
  const at = runAtMs(run);
  const ingested = run.ingestState === "ingested";
  return (
    <Card padding="none" data-testid="tests-run-facts">
      <dl className="grid grid-cols-2 gap-x-6 gap-y-3 p-4 sm:grid-cols-3 lg:grid-cols-6">
        <Fact label="Status">
          <Status tone={verdict.tone} label={verdict.words} />
        </Fact>
        <Fact label="Lane">
          <span className="font-mono text-code">{run.lane || "—"}</span>
        </Fact>
        <Fact label="Source">{runSourceLabel(run)}</Fact>
        <Fact label="Commit">
          <span className="flex flex-wrap items-center gap-1.5">
            <Link href={hrefs.runs({ commit: run.gitCommit })} className={cn(linkClass, "font-mono text-code")} title={`Every run of ${run.gitCommit}`}>
              {shortSha(run.gitCommit) || "—"}
            </Link>
            {run.dirty && <Tag title="Recorded from a checkout with uncommitted changes: it never counts toward its commit">uncommitted</Tag>}
          </span>
        </Fact>
        <Fact label="Branch">
          {run.branch ? (
            <Link href={hrefs.runs({ branch: run.branch })} className={cn(linkClass, "block truncate font-mono text-code")} title={`Every run of ${run.branch}`}>
              {run.branch}
            </Link>
          ) : (
            <span className="text-ink-muted">none</span>
          )}
        </Fact>
        <Fact label="CI run">
          {run.ci?.runUrl ? (
            <OutLink href={run.ci.runUrl} testId="tests-run-ci">
              {run.ci.runId ? `#${run.ci.runId}` : "CI run"}
              {run.ci.attempt > 1 ? ` (attempt ${run.ci.attempt})` : ""}
            </OutLink>
          ) : (
            <span className="text-ink-muted">—</span>
          )}
        </Fact>
        <Fact label="Ran">{at === null ? "—" : <span title={formatWhen(at, now)}>{formatAgo(at, now)}</span>}</Fact>
        <Fact label="Duration">{took === null ? "—" : formatDuration(took)}</Fact>
        <Fact label="Coverage">{run.coveragePercent === null ? <span className="text-ink-muted">none recorded</span> : formatPercent(run.coveragePercent)}</Fact>
        <Fact label="Exit code">
          <span className="font-mono text-code">{run.exitCode}</span>
        </Fact>
        <Fact label="Uploaded by">{run.uploadedBy || <span className="text-ink-muted">—</span>}</Fact>
        <Fact label="forge">{run.forgeVersion ? <span className="font-mono text-code">{run.forgeVersion}</span> : <span className="text-ink-muted">—</span>}</Fact>
      </dl>
      {ingested && (
        <div className="grid grid-cols-2 divide-border-subtle border-t border-border-subtle sm:grid-cols-5 sm:divide-x" data-testid="tests-run-totals">
          <Figure label="Passed" value={run.totals.passed.toLocaleString()} testId="tests-run-passed" />
          <Figure label="Failed" value={run.totals.failed.toLocaleString()} testId="tests-run-failed" tone={run.totals.failed > 0 ? "danger" : undefined} />
          <Figure label="Skipped" value={run.totals.skipped.toLocaleString()} testId="tests-run-skipped" />
          <Figure label="Retry passes" value={run.totals.retryPassed.toLocaleString()} testId="tests-run-retry" tone={run.totals.retryPassed > 0 ? "warning" : undefined} />
          <Figure label="Packages" value={packageCount > 0 ? packageCount.toLocaleString() : "—"} testId="tests-run-packages" />
        </div>
      )}
    </Card>
  );
}

function FailureRow({ failure, index, hrefs }: { failure: TestFailure; index: number; hrefs: TestsRunHrefs }) {
  const verdict = failureVerdictWords(failure.verdict);
  const place = failurePlace(failure);
  return (
    <li className="space-y-2 px-4 py-3" data-testid={`tests-run-failure-${index}`} data-verdict={failure.verdict}>
      <div className="flex flex-wrap items-start justify-between gap-x-4 gap-y-1">
        <Link href={hrefs.test(failure.test.id)} className="min-w-0 max-w-full hover:underline" title="This test's outcomes, flake record and failure signatures">
          <TestName test={failure.test} />
        </Link>
        <span className="flex flex-wrap items-center gap-2 text-meta text-ink-muted">
          {verdict && (
            <Tag tone={verdict.words.startsWith("Real") || failure.verdict === "raced" ? "danger" : "neutral"} title={verdict.title}>
              {verdict.words}
            </Tag>
          )}
          {failure.attempts > 1 && <span>{failure.attempts} attempts</span>}
        </span>
      </div>
      {place && <p className="break-all font-mono text-meta text-ink-muted">{place}</p>}
      <FailureOutput output={failure.output} open={index === 0} testId={`tests-run-failure-${index}-output`} />
    </li>
  );
}

function Failures({ failures, failed, hrefs }: { failures: TestFailure[]; failed: number; hrefs: TestsRunHrefs }) {
  const uncounted = failures.filter((f) => f.verdict === "host" || f.verdict === "correlated").length;
  const note =
    failures.length === 0
      ? undefined
      : uncounted > 0
        ? `${uncounted} of ${failures.length} ${uncounted === 1 ? "is a host or infrastructure failure" : "are host or infrastructure failures"}, never counted against ${uncounted === 1 ? "its test" : "their tests"}`
        : `${failures.length} ${failures.length === 1 ? "failure" : "failures"}`;
  return (
    <Section title="Failures" note={note} testId="tests-run-failures" flush>
      {failures.length === 0 ? (
        <p className="px-4 pb-4 text-ui text-ink-muted" data-testid="tests-run-no-failures">
          {failed > 0 ? `The run counted ${failed} failed, but recorded no failure output.` : "No test failed."}
        </p>
      ) : (
        <ul className="divide-y divide-border-subtle border-t border-border-subtle">
          {failures.map((failure, index) => (
            <FailureRow key={`${failure.test.id}|${index}`} failure={failure} index={index} hrefs={hrefs} />
          ))}
        </ul>
      )}
    </Section>
  );
}

function RetryPasses({ detail, hrefs }: { detail: TestRunDetail; hrefs: TestsRunHrefs }) {
  return (
    <Section title="Retry passes" note="Failed, then passed on a retry: a flake signal" testId="tests-run-retry-passes">
      {detail.retryPasses.length === 0 ? (
        <p className="text-ui text-ink-muted">No test needed a retry.</p>
      ) : (
        <ul className="space-y-2">
          {detail.retryPasses.map((test) => (
            <li key={test.id}>
              <Link href={hrefs.test(test.id)} className="block hover:underline">
                <TestName test={test} />
              </Link>
            </li>
          ))}
        </ul>
      )}
    </Section>
  );
}

function Slowest({ detail, hrefs }: { detail: TestRunDetail; hrefs: TestsRunHrefs }) {
  return (
    <Section title="Slowest tests" testId="tests-run-slowest">
      {detail.slowest.length === 0 ? (
        <p className="text-ui text-ink-muted">The run recorded no timings.</p>
      ) : (
        <ol className="space-y-2">
          {detail.slowest.map(({ test, durationMs }) => (
            <li key={test.id} className="flex items-start justify-between gap-4">
              <Link href={hrefs.test(test.id)} className="min-w-0 hover:underline">
                <TestName test={test} />
              </Link>
              <span className="shrink-0 tabular-nums text-ink">{formatDuration(durationMs)}</span>
            </li>
          ))}
        </ol>
      )}
    </Section>
  );
}

function ZeroEvidence({ detail }: { detail: TestRunDetail }) {
  const ran = detail.packageCount > 0 ? `of the ${detail.packageCount} packages it ran` : "of the packages it ran";
  return (
    <Section title="Packages with no tests recorded" note={`Ran, and recorded no test: ${ran}`} testId="tests-run-zero-evidence">
      {detail.zeroEvidencePackages.length === 0 ? (
        <p className="text-ui text-ink-muted">Every package it ran recorded a test.</p>
      ) : (
        <>
          <ul className="flex flex-wrap gap-1.5">
            {detail.zeroEvidencePackages.map((pkg) => (
              <li key={pkg}>
                <code className="rounded-md border border-border/60 bg-surface-sunken px-1.5 py-0.5 font-mono text-code text-ink">{pkg}</code>
              </li>
            ))}
          </ul>
          <p className="mt-2 text-meta text-ink-muted">A package whose tests were all filtered out, or that failed to build, looks like this. It is not evidence that its tests passed.</p>
        </>
      )}
    </Section>
  );
}
