// Copyright (c) 2026 Reliant Labs

/**
 * ListTestRuns' filters as the server applies them, over WIRE run summaries:
 * the one rule every in-memory answer shares (runFlakeFixtures.ts for the
 * Tests pages' scenarios, and the shop pack's TestsService handler over
 * fixtures/shop/model/tests.ts). A leaf over wire.ts, so the demo can use it
 * without loading the scenario fixtures.
 */

import type { RunsRequest } from "@/services/controlPlane/tests/types";
import { WireIngestState, WireRunOutcome, WireRunSource, type WireRunSummary } from "@/services/controlPlane/tests/wire";

const SOURCE = { ci: WireRunSource.CI, local: WireRunSource.LOCAL, env: WireRunSource.ENV, confirm: WireRunSource.CONFIRM } as const;
const OUTCOME = { passed: WireRunOutcome.PASSED, failed: WireRunOutcome.FAILED, undetermined: WireRunOutcome.UNDETERMINED } as const;

/** Whether `r` passes `filters`. A release names the commit it was cut from (`releaseCommitOf`; null: no such release). */
export function runMatchesFilters(r: WireRunSummary, filters: RunsRequest["filters"], releaseCommitOf: (version: string) => string | null): boolean {
  if (filters.environmentId && r.environmentId !== filters.environmentId) return false;
  if (filters.branch && r.branch !== filters.branch) return false;
  if (filters.gitCommit && !r.gitCommit.startsWith(filters.gitCommit)) return false;
  if (filters.releaseVersion) {
    const commit = releaseCommitOf(filters.releaseVersion);
    if (!commit || r.gitCommit !== commit) return false;
  }
  if (filters.lane && r.lane !== filters.lane) return false;
  if (filters.source && r.source !== SOURCE[filters.source]) return false;
  if (filters.outcome) {
    // What the page shows: a run still being ingested is undetermined, whatever it will be.
    const outcome = r.ingestState === WireIngestState.INGESTED ? r.outcome : WireRunOutcome.UNDETERMINED;
    if ((outcome === WireRunOutcome.UNSPECIFIED ? WireRunOutcome.UNDETERMINED : outcome) !== OUTCOME[filters.outcome]) return false;
  }
  return true;
}
