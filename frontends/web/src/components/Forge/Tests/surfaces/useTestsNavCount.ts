// Copyright (c) 2026 Reliant Labs

import { useTestSummary } from "@/hooks/test-queries";

import { testsAttentionCount } from "./surfacesModel";

/**
 * The Tests nav group's badge count for `project`: the console nav model's
 * `navCounts.tests` (useConsoleNavCounts), drawn by navConfig/tests.tsx.
 * The default branch's flaky tests, plus its suspected ones when the
 * project acts at `suspected` (surfacesModel.ts testsAttentionCount; the
 * policy is the summary's, flaky-only while the server does not say).
 *
 * The default branch's summary is the Tests Overview's own read, so it costs
 * no request of its own. 0 while it loads, and on any failure: a control
 * plane without TestsService answers Unimplemented, which is never retried.
 */
export function useTestsNavCount(project: string): number {
  const summary = useTestSummary(project);
  return testsAttentionCount(summary.data, summary.data?.flakePolicy ?? "flaky");
}
