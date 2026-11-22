// Copyright (c) 2026 Reliant Labs

/**
 * A release's Tests tab (release detail, `?tab=tests`): GetReleaseTests —
 * the report of the commit the release was cut from, and what changed since
 * the release before it (forge docs/proposals/tests-coverage-flakes.md §3.2).
 *
 *   status     ok (every lane merged), partial (the reason names what is
 *              missing) or unavailable (no report for the commit: a designed
 *              state that says why, never an error)
 *   figures    combined coverage with its delta against the previous
 *              release, the totals, and the lanes merged
 *   per lane   each lane's coverage, one bar each
 *   since      the tests that started failing, and that flaked, since the
 *              previous release
 *   links      Runs and Coverage pinned to the release's commit
 *
 * The tab belongs to no environment: a release has one commit, and so one
 * report, wherever it runs.
 */

import { ArrowRight, FlaskConical } from "lucide-react";

import Link from "@/components/forge-ui/link";
import Mono from "@/components/forge-ui/mono";
import Status from "@/components/forge-ui/status";
import { useReleaseTests } from "@/hooks/test-queries";
import type { ReleaseTests, TestFlake, TestKey } from "@/services/controlPlane/tests/types";
import { cn } from "@/lib/utils";

import { flakyTestPath, testsPath, withSearch } from "../../Console/paths";
import { formatAgo } from "../../Observability/format";
import { StateFrame } from "../../Observability/ObsStates";
import { Note, Panel } from "../../Releases/releaseParts";
import { shortVersion } from "../../Releases/releaseModel";
import { FLAKE_STATE } from "../flakyModel";
import { TestName } from "../TestsParts";
import { TestsError } from "../TestsStates";
import { formatDelta, formatPercent, laneColor, orderLanes, shortSha } from "../testsModel";
import { releaseDeltaWords, releaseTestsMark } from "./surfacesModel";

export interface ReleaseTestsHrefs {
  /** Tests › Runs, narrowed to one commit. */
  runs: (commit: string) => string;
  /** Tests › Coverage at one commit. */
  coverage: (commit: string) => string;
  /** One test's page. */
  test: (testId: string) => string;
  /** Another release's page (the previous one). */
  release?: (version: string) => string;
}

/** The console's links for `project`: the Tests pages, pinned to a commit. */
export function releaseTestsHrefs(project: string, release?: (version: string) => string): ReleaseTestsHrefs {
  return {
    runs: (commit) => withSearch(testsPath(project, "runs"), { commit }),
    coverage: (commit) => withSearch(testsPath(project, "coverage"), { commit }),
    test: (testId) => flakyTestPath(project, testId),
    release,
  };
}

export function ReleaseTestsTab({ project, version, gitCommit, hrefs, now }: { project: string; version: string; gitCommit?: string; hrefs: ReleaseTestsHrefs; now?: number }) {
  const tests = useReleaseTests(project, version);
  if (tests.isLoading) {
    return (
      <Panel title="Tests" testId="release-tests-loading">
        <div className="space-y-3" aria-busy="true" aria-label="Loading the release's tests">
          <div className="h-8 w-40 animate-pulse rounded-md bg-ink/5 motion-reduce:animate-none" />
          <div className="h-3 animate-pulse rounded-md bg-ink/5 motion-reduce:animate-none" />
          <div className="h-3 w-2/3 animate-pulse rounded-md bg-ink/5 motion-reduce:animate-none" />
        </div>
      </Panel>
    );
  }
  if (tests.error || !tests.data) {
    return <TestsError error={tests.error} subject={`${shortVersion(version)}'s tests`} onRetry={() => void tests.refetch()} retrying={tests.isFetching} testId="release-tests-error" />;
  }
  return <ReleaseTestsView tests={tests.data} fallbackCommit={gitCommit ?? ""} hrefs={hrefs} now={now ?? Date.now()} />;
}

const LINK = "no-color inline-flex h-7 items-center gap-1 rounded-md px-2 text-meta text-ink-muted hover:bg-ink/5 hover:text-ink";

function CommitLinks({ commit, hrefs }: { commit: string; hrefs: ReleaseTestsHrefs }) {
  if (!commit) return null;
  return (
    <div className="-ml-2 flex flex-wrap items-center gap-1" data-testid="release-tests-links">
      <Link href={hrefs.runs(commit)} className={LINK} data-testid="release-tests-runs-link">
        Runs for {shortSha(commit)}
        <ArrowRight className="h-3 w-3" aria-hidden="true" />
      </Link>
      <Link href={hrefs.coverage(commit)} className={LINK} data-testid="release-tests-coverage-link">
        Coverage at {shortSha(commit)}
        <ArrowRight className="h-3 w-3" aria-hidden="true" />
      </Link>
    </div>
  );
}

export function ReleaseTestsView({ tests, fallbackCommit, hrefs, now }: { tests: ReleaseTests; fallbackCommit: string; hrefs: ReleaseTestsHrefs; now: number }) {
  const commit = tests.gitCommit || fallbackCommit;
  const report = tests.report;

  // UNAVAILABLE: a state with its reason. A release nobody tested is common, not broken.
  if (tests.status === "unavailable" || !report) {
    return (
      <div className="space-y-3" data-testid="release-tests" data-status="unavailable">
        <StateFrame testId="release-tests-unavailable" icon={<FlaskConical className="h-4 w-4" />} title="No test report for this release">
          <p data-testid="release-tests-reason">{tests.reason || "No test report for this commit."}</p>
          <p>
            A release is judged by the runs recorded for the commit it was cut from
            {commit ? (
              <>
                {" "}
                (<code className="font-mono text-ink">{shortSha(commit)}</code>)
              </>
            ) : null}
            . Runs uploaded with <code className="font-mono text-ink">reliant forge test report --upload</code> for that commit appear here.
          </p>
        </StateFrame>
        <CommitLinks commit={commit} hrefs={hrefs} />
      </div>
    );
  }

  const mark = releaseTestsMark(tests);
  const coverage = report.coverage;
  const lanes = orderLanes(Object.keys(coverage?.lanes ?? {}));
  const prev = tests.previousReleaseVersion;
  const delta = tests.coverageDeltaPercent;

  return (
    <div className="space-y-4" data-testid="release-tests" data-status={tests.status}>
      {tests.status === "partial" && (
        <Note tone="warning" testId="release-tests-partial">
          <span className="font-medium text-ink">Partial evidence.</span> {tests.reason || `Only ${report.lanes.join(", ")} recorded for this commit.`}
        </Note>
      )}

      <Panel
        title="Tests"
        meta={
          <>
            {commit && <Mono value={commit} />} · {report.lanes.length} {report.lanes.length === 1 ? "lane" : "lanes"} from {report.runCount}{" "}
            {report.runCount === 1 ? "run" : "runs"}
            {report.updatedMs !== null && <> · updated {formatAgo(report.updatedMs, now)}</>}
          </>
        }
        testId="release-tests-summary"
      >
        <dl className="grid grid-cols-1 gap-4 sm:grid-cols-3">
          <div className="min-w-0 space-y-1" data-testid="release-tests-coverage">
            <dt className="text-meta font-medium text-ink-muted">Coverage</dt>
            <dd className="text-2xl font-semibold tabular-nums text-ink">{coverage?.combined && coverage.combined.statements > 0 ? formatPercent(coverage.combined.percent) : "—"}</dd>
            <dd className="text-meta text-ink-muted" data-testid="release-tests-delta">
              {delta !== null && prev ? (
                <>
                  <span className={cn("font-medium", delta < 0 ? "text-danger-ink" : "text-ink")}>{formatDelta(delta)}</span> since{" "}
                  {hrefs.release ? (
                    <Link href={hrefs.release(prev)} className="no-color text-ink underline-offset-2 hover:underline" title={prev}>
                      {shortVersion(prev)}
                    </Link>
                  ) : (
                    shortVersion(prev)
                  )}
                </>
              ) : (
                releaseDeltaWords(tests, shortVersion)
              )}
            </dd>
          </div>
          <div className="min-w-0 space-y-1" data-testid="release-tests-totals">
            <dt className="text-meta font-medium text-ink-muted">Result</dt>
            <dd className="text-2xl font-semibold">
              <span title={mark.title}>
                <Status tone={mark.tone} label={mark.words} />
              </span>
            </dd>
            <dd className="text-meta tabular-nums text-ink-muted">
              {report.totals.passed.toLocaleString()} passed
              {report.totals.failed > 0 && <span className="text-danger-ink"> · {report.totals.failed.toLocaleString()} failed</span>}
              {report.totals.skipped > 0 && <> · {report.totals.skipped.toLocaleString()} skipped</>}
              {report.totals.retryPassed > 0 && <> · {report.totals.retryPassed.toLocaleString()} passed on retry</>}
            </dd>
          </div>
          <div className="min-w-0 space-y-1">
            <dt className="text-meta font-medium text-ink-muted">Lanes merged</dt>
            <dd className="text-ui font-medium text-ink" data-testid="release-tests-lanes">
              {orderLanes(report.lanes).join(" · ") || "none"}
            </dd>
            {coverage && coverage.excludedGeneratedStatements > 0 && (
              <dd className="text-meta text-ink-muted">{coverage.excludedGeneratedStatements.toLocaleString()} generated statements left out</dd>
            )}
          </div>
        </dl>
      </Panel>

      {lanes.length > 0 && (
        <Panel title="Coverage per lane" meta="statements each lane reached" testId="release-tests-per-lane">
          <ul className="space-y-2">
            {lanes.map((lane) => {
              const figure = coverage!.lanes[lane]!;
              return (
                <li key={lane} className="grid grid-cols-[6rem_minmax(0,1fr)_3.5rem] items-center gap-3 text-ui" data-testid={`release-tests-lane-${lane}`}>
                  <span className="truncate text-ink">{lane}</span>
                  <span className="h-1.5 overflow-hidden rounded-full bg-surface-sunken" aria-hidden="true">
                    {/* Data viz: the lane's chart token, the same hue it has on the Tests pages. */}
                    <span className="block h-full rounded-full" style={{ width: `${Math.min(100, Math.max(0, figure.percent))}%`, backgroundColor: `hsl(var(${laneColor(lane)}))` }} />
                  </span>
                  <span className="text-right tabular-nums text-ink">{formatPercent(figure.percent)}</span>
                </li>
              );
            })}
          </ul>
        </Panel>
      )}

      <SinceLast tests={tests} hrefs={hrefs} />
      <CommitLinks commit={commit} hrefs={hrefs} />
    </div>
  );
}

function SinceLast({ tests, hrefs }: { tests: ReleaseTests; hrefs: ReleaseTestsHrefs }) {
  const prev = tests.previousReleaseVersion;
  const since = prev ? `since ${shortVersion(prev)}` : "in this release";
  if (tests.newFailures.length === 0 && tests.newFlakes.length === 0) {
    return (
      <p className="text-ui text-ink-muted" data-testid="release-tests-since-none">
        {prev ? `No test started failing or flaking since ${shortVersion(prev)}.` : "No failing or flaky test in this release's report."}
      </p>
    );
  }
  return (
    <Panel title={`New ${since}`} flush testId="release-tests-since">
      <ul className="divide-y divide-border-subtle border-t border-border-subtle">
        {tests.newFailures.map((test) => (
          <SinceRow key={`fail-${test.id}`} test={test} href={hrefs.test(test.id)} tone="danger" label="Started failing" />
        ))}
        {tests.newFlakes.map((flake: TestFlake) => (
          <SinceRow key={`flake-${flake.test.id}`} test={flake.test} href={hrefs.test(flake.test.id)} tone={FLAKE_STATE[flake.state].tone} label={FLAKE_STATE[flake.state].label} />
        ))}
      </ul>
    </Panel>
  );
}

function SinceRow({ test, href, tone, label }: { test: TestKey; href: string; tone: "success" | "warning" | "danger" | "neutral"; label: string }) {
  return (
    <li>
      <Link href={href} className="no-color flex items-center gap-3 px-4 py-2 hover:bg-ink/5" data-testid={`release-tests-since-${test.id}`}>
        <TestName test={test} className="flex-1" />
        <Status tone={tone} label={label} size="sm" />
      </Link>
    </li>
  );
}
