// Copyright (c) 2026 Reliant Labs

/**
 * Every Tests page is built: Coverage (TestsCoveragePage), Runs, one run,
 * Flaky tests and one test (TestsRunsPage, TestsRunPage, TestsFlakyPage,
 * TestsFlakyTestPage). This module stays only so the routes and the preview
 * that import Coverage from it keep working.
 */

/** Tests › Coverage is built (W2): its own module, re-exported so the routes keep their import. */
export { TestsCoveragePage } from "./TestsCoveragePage";
