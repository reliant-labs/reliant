// Copyright (c) 2026 Reliant Labs

import { useTestSummary } from "@/hooks/test-queries";

import { testsAttentionCount } from "./surfacesModel";

/**
 * The Tests nav group's badge count for `project` (suspected plus flaky on
 * the default branch): the console nav model reads it once per app, and the
 * nav's Tests group (navConfig/tests.tsx) draws it. The default branch's
 * summary is the Tests Overview's own read, so it costs no extra request
 * there. 0 while it loads, and on any failure: a control plane without
 * TestsService answers Unimplemented, which is never retried.
 */
export function useTestsNavCount(project: string): number {
  const summary = useTestSummary(project);
  return testsAttentionCount(summary.data);
}
