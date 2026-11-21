// Copyright (c) 2026 Reliant Labs

/**
 * The Tests pages that come after the Overview, registered now so the nav
 * and every link to them work: Coverage (W2: the tree table and a file's
 * annotated lines), Runs and one run, Flaky tests and one test (W3).
 *
 * Each says what it will show and where those figures are meanwhile (the
 * Overview, the CLI). No fake rows: the data layer under them is ready
 * (hooks/test-queries.ts useCoverage, useFileCoverage, useTestRuns,
 * useTestRun, useFlakyTests, useTest), and their pages replace this file's
 * components one by one.
 */

import { useParams } from "@tanstack/react-router";

import Link from "@/components/forge-ui/link";

import { useForgeConsole } from "../Console/consoleContext";
import { CommandLine } from "../GettingStarted";
import { TestsFrame } from "./testsScope";
import { TestsPageComing } from "./TestsStates";

function OverviewLink() {
  const { href } = useForgeConsole();
  return (
    <p>
      Meanwhile the{" "}
      <Link href={href({ kind: "tests" })} className="font-medium text-ink underline-offset-2 hover:underline">
        Tests overview
      </Link>{" "}
      has the newest report&apos;s figures, and the CLI reads the rest:
    </p>
  );
}

/** Tests › Coverage is built (W2): its own module, re-exported so the routes keep their import. */
export { TestsCoveragePage } from "./TestsCoveragePage";

export function TestsRunsPage() {
  return (
    <TestsFrame title="Runs" testId="console-tests-runs">
      <TestsPageComing
        testId="tests-runs-coming"
        title="The runs list"
        shows="Every recorded run, newest first: commit, branch, where it ran (CI, a local checkout or a preview environment), its lanes, passed, failed and skipped, duration and coverage."
      >
        <OverviewLink />
        <CommandLine command="reliant forge test runs" />
      </TestsPageComing>
    </TestsFrame>
  );
}

export function TestsRunPage() {
  const { run } = useParams({ strict: false }) as { run?: string };
  return (
    <TestsFrame title="Run" subtitle={run ? <code className="font-mono">{run}</code> : undefined} testId="console-tests-run">
      <TestsPageComing
        testId="tests-run-coming"
        title="A run's page"
        shows="A run's failures with their output and file:line, its retry passes, its slowest tests and the packages that recorded no test."
      >
        <OverviewLink />
        <CommandLine command={`reliant forge test report${run ? ` ${run}` : ""}`} />
      </TestsPageComing>
    </TestsFrame>
  );
}

export function TestsFlakyPage() {
  return (
    <TestsFrame title="Flaky tests" testId="console-tests-flaky">
      <TestsPageComing
        testId="tests-flaky-coming"
        title="Flaky test triage"
        shows="Tests that are suspected, flaky or quarantined, by flake rate with its n, the signals that caught them and their issues, with host and infrastructure failures listed apart and never counted against a test."
      >
        <OverviewLink />
        <CommandLine command="reliant forge test flakes" />
      </TestsPageComing>
    </TestsFrame>
  );
}

export function TestsFlakyTestPage() {
  const { test } = useParams({ strict: false }) as { test?: string };
  return (
    <TestsFrame title="Flaky test" subtitle={test ? <code className="break-all font-mono">{test}</code> : undefined} testId="console-tests-flaky-test">
      <TestsPageComing
        testId="tests-flaky-test-coming"
        title="A test's flake record"
        shows="A test's outcomes over its last 50 runs, its failure signatures grouped, the commits where it split, and its quarantine entry and issue."
      >
        <OverviewLink />
        <CommandLine command="reliant forge test flakes --state flaky" />
      </TestsPageComing>
    </TestsFrame>
  );
}
