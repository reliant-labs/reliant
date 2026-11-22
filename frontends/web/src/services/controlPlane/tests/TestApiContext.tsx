// Copyright (c) 2026 Reliant Labs

/**
 * The seam between the Tests pages (components/Forge/Tests) and the
 * TestsService reads they make.
 *
 * The same shape as VulnerabilityApiContext: component tests and the preview
 * hand it the in-memory fixture (components/Forge/Tests/fixtures.ts), and
 * production never provides anything but the real client (testApi.ts), which
 * calls the control plane directly with the user's session.
 */

import { createContext, useContext, type ReactNode } from "react";

import * as tests from "./testApi";

export interface TestApi {
  getTestSummary: typeof tests.getTestSummary;
  listTestRuns: typeof tests.listTestRuns;
  getTestRun: typeof tests.getTestRun;
  getCoverage: typeof tests.getCoverage;
  getFileCoverage: typeof tests.getFileCoverage;
  getReleaseTests: typeof tests.getReleaseTests;
  listFlakyTests: typeof tests.listFlakyTests;
  getTest: typeof tests.getTest;
}

export const controlPlaneTestApi: TestApi = {
  getTestSummary: tests.getTestSummary,
  listTestRuns: tests.listTestRuns,
  getTestRun: tests.getTestRun,
  getCoverage: tests.getCoverage,
  getFileCoverage: tests.getFileCoverage,
  getReleaseTests: tests.getReleaseTests,
  listFlakyTests: tests.listFlakyTests,
  getTest: tests.getTest,
};

const TestApiContext = createContext<TestApi>(controlPlaneTestApi);

export function TestApiProvider({ api, children }: { api: TestApi; children: ReactNode }) {
  return <TestApiContext.Provider value={api}>{children}</TestApiContext.Provider>;
}

export function useTestApi(): TestApi {
  return useContext(TestApiContext);
}
