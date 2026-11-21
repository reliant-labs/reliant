// Copyright (c) 2026 Reliant Labs

/**
 * The Tests page that comes after the Overview and is not built yet,
 * registered now so the nav and every link to it work: Coverage (W2: the
 * tree table and a file's annotated lines). Runs, one run, Flaky tests and
 * one test have their own pages (TestsRunsPage, TestsRunPage, TestsFlakyPage,
 * TestsFlakyTestPage).
 *
 * It says what it will show and where those figures are meanwhile (the
 * Overview, the CLI). No fake rows: the data layer under it is ready
 * (hooks/test-queries.ts useCoverage, useFileCoverage).
 */

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

export function TestsCoveragePage() {
  return (
    <TestsFrame title="Coverage" testId="console-tests-coverage">
      <TestsPageComing
        testId="tests-coverage-coming"
        title="Coverage browsing"
        shows="Coverage per package and file, combined and per lane, with diff coverage against a base commit, and each file's lines annotated with their hits (source fetched from your linked repository, never stored)."
      >
        <OverviewLink />
        <CommandLine command="reliant forge test coverage --by package" />
      </TestsPageComing>
    </TestsFrame>
  );
}
