// Copyright (c) 2026 Reliant Labs

/**
 * GetReleaseTests' fixtures: what the release Tests tab, the Releases list's
 * Tests column and the preview read, as WIRE messages (wire.ts maps them, as
 * the real client will). The versions are shopfront's releases in the
 * preview (e2e/visual/fixtures/services.ts) plus the two on the Tests
 * Overview's trend (fixtures.ts FIXTURE_RELEASES), so every surface agrees:
 *
 *   20261118.144000-f6e5…  staging: cut from main's newest commit (9f3c…),
 *                          the full three-lane report, +0.8 on prod's
 *   20261118.121200-a1b2…  prod: three lanes, +1.6 on the release before it,
 *                          two tests flaked since
 *   20261117.090000-0bad…  partial: e2e never recorded, two new failures;
 *                          the release before it has no report, so no delta
 *   20261116.160500-5eed…  unavailable: no run was recorded for its commit
 *   20261105.093000-77e0…  the first release with a report: nothing to compare
 *
 * Any other version is unavailable ("no test report for this commit"), what
 * the server answers for a release whose commit nobody tested.
 */

import { Code, ConnectError } from "@connectrpc/connect";

import type { ReleaseTestsRequest } from "@/services/controlPlane/tests/types";
import { WireEvidenceStatus, type WireCommitReport, type WireReleaseTestsResponse, type WireStatementCoverage } from "@/services/controlPlane/tests/wire";

import { FIXTURE_COMMIT, FIXTURE_REPORT, fixtureSummaryResponse, type TestsScenario } from "./fixtures";
import { fixtureFlakes, fixtureTestKey } from "./runFlakeFixtures";

/**
 * The preview's shopfront releases (e2e/visual/fixtures/services.ts), newest
 * first. Literals, not FIXTURE_RELEASES: fixtures.ts imports this module, so
 * its constants are not initialised yet when this one evaluates
 * (__tests__/releaseTestsSurfaces.test.tsx holds prod and first equal to them).
 */
export const FIXTURE_RELEASE_VERSIONS = {
  staging: "20261118.144000-f6e5d4c3b2a1",
  prod: "20261118.121200-a1b2c3d4e5f6",
  partial: "20261117.090000-0badc0ffee00",
  untested: "20261116.160500-5eed5eed5eed",
  first: "20261105.093000-77e0c1d2a3b4",
} as const;

const STATEMENTS = 48_210;

function stmts(percent: number, statements = STATEMENTS): WireStatementCoverage {
  return { statements: BigInt(statements), covered: BigInt(Math.round((statements * percent) / 100)), percent };
}

/** A commit's report: `lanes` by percent, the combined figure, and its totals. */
function report(gitCommit: string, combined: number, lanes: Record<string, number>, totals: [number, number, number, number]): WireCommitReport {
  return {
    ...FIXTURE_REPORT,
    gitCommit,
    lanes: Object.keys(lanes),
    runCount: Object.keys(lanes).length * 2,
    totals: { passed: BigInt(totals[0]), failed: BigInt(totals[1]), skipped: BigInt(totals[2]), retryPassed: BigInt(totals[3]) },
    coverage: {
      ...FIXTURE_REPORT.coverage!,
      combined: stmts(combined),
      lanes: Object.fromEntries(Object.entries(lanes).map(([lane, percent]) => [lane, stmts(percent)])),
      onlyE2e: lanes.e2e === undefined ? undefined : stmts(3.0),
      // A release's delta is against the previous release (the response's), not the commit's base report.
      deltaPercent: undefined,
      baseHasReport: false,
    },
  };
}

const flake = (name: string) => fixtureFlakes().find((candidate) => candidate.test?.name === name)!;

function unavailable(version: string, reason: string): WireReleaseTestsResponse {
  return {
    releaseId: `rel-${version}`,
    releaseVersion: version,
    gitCommit: version.split("-").pop() ?? "",
    status: WireEvidenceStatus.UNAVAILABLE,
    reason,
    previousReleaseVersion: "",
    newFailures: [],
    newFlakes: [],
  };
}

/** GetReleaseTests for `version` in the report scenario. */
function releaseTests(version: string): WireReleaseTestsResponse {
  const markers = fixtureSummaryResponse().releaseMarkers;
  const v = FIXTURE_RELEASE_VERSIONS;
  const base = (fields: Partial<WireReleaseTestsResponse> & Pick<WireReleaseTestsResponse, "gitCommit" | "report">): WireReleaseTestsResponse => ({
    releaseId: `rel-${version}`,
    releaseVersion: version,
    status: WireEvidenceStatus.OK,
    reason: "",
    previousReleaseVersion: "",
    newFailures: [],
    newFlakes: [],
    ...fields,
  });
  switch (version) {
    case v.staging:
      return base({
        gitCommit: FIXTURE_COMMIT,
        report: FIXTURE_REPORT,
        previousReleaseVersion: v.prod,
        coverageDeltaPercent: 0.8,
        newFlakes: [flake("TestCheckoutTimeout")],
      });
    case v.prod:
      return base({
        gitCommit: markers[1]!.gitCommit,
        report: report(markers[1]!.gitCommit, 73.4, { unit: 60.9, integration: 37.8, e2e: 52.0 }, [1398, 0, 36, 1]),
        previousReleaseVersion: v.partial,
        coverageDeltaPercent: 1.6,
        newFlakes: [flake("TestSyncJob"), flake("TestCartMerge")],
      });
    case v.partial:
      return base({
        gitCommit: "0badc0ffee0012ab34cd56ef7890ab12cd34ef56",
        status: WireEvidenceStatus.PARTIAL,
        reason: "The e2e lane was not recorded for this commit: only unit and integration are merged.",
        report: report("0badc0ffee0012ab34cd56ef7890ab12cd34ef56", 71.8, { unit: 60.2, integration: 37.1 }, [1180, 2, 31, 0]),
        previousReleaseVersion: v.untested,
        newFailures: [fixtureTestKey("internal/billing", "TestRefundPartial", 41), fixtureTestKey("internal/webhooks", "TestWebhookRetry", 63)],
      });
    case v.untested:
      return unavailable(version, "No test report for commit 5eed5eed5eed: no run was recorded for it.");
    case v.first:
      return base({
        gitCommit: markers[0]!.gitCommit,
        report: report(markers[0]!.gitCommit, 70.6, { unit: 59.8, integration: 36.9, e2e: 51.2 }, [1301, 0, 33, 0]),
      });
    default:
      return unavailable(version, "No test report for this commit.");
  }
}

/** GetReleaseTests in `scenario`: the report scenarios answer by version; an empty record has no report for any release. */
export function fixtureReleaseTestsWire(scenario: TestsScenario, req: ReleaseTestsRequest): WireReleaseTestsResponse {
  if (!req.version) throw new ConnectError("a release version is required", Code.InvalidArgument);
  switch (scenario) {
    case "report":
      return releaseTests(req.version);
    case "no-flakes":
      return { ...releaseTests(req.version), newFlakes: [] };
    case "pending":
      return unavailable(req.version, "Its commit's runs are still being processed.");
    default:
      return unavailable(req.version, "No test report for this commit.");
  }
}
