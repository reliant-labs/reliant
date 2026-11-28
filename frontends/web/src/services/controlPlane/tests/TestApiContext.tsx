// Copyright (c) 2026 Reliant Labs

/**
 * The seam between the Tests pages (components/Forge/Tests) and the
 * TestsService reads they make.
 *
 * Component tests and the preview
 * hand it the in-memory fixture (components/Forge/Tests/fixtures.ts), and
 * production never provides anything but the real client (testApi.ts), which
 * calls the control plane directly with the user's session. The default is
 * that client on the nearest TransportScope's transport, else on the app's.
 */

import { createContext, useContext, type ReactNode } from "react";
import type { Transport } from "@connectrpc/connect";

import { transport as appTransport } from "@/lib/connect";
import { useTransport } from "@/lib/transportScope";

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

/**
 * The real client on `transport`: a TransportScope's (lib/transportScope.tsx),
 * or undefined for the app's. Each method hands it to its RPC function, which
 * passes it to getControlPlaneClient.
 */
function apiOn(transport: Transport | undefined): TestApi {
  return {
    getTestSummary: (req, opts) => tests.getTestSummary(req, opts, transport),
    listTestRuns: (req, opts) => tests.listTestRuns(req, opts, transport),
    getTestRun: (req, opts) => tests.getTestRun(req, opts, transport),
    getCoverage: (req, opts) => tests.getCoverage(req, opts, transport),
    getFileCoverage: (req, opts) => tests.getFileCoverage(req, opts, transport),
    getReleaseTests: (req, opts) => tests.getReleaseTests(req, opts, transport),
    listFlakyTests: (req, opts) => tests.listFlakyTests(req, opts, transport),
    getTest: (req, opts) => tests.getTest(req, opts, transport),
  };
}

/** The real client on the app's transport (lib/connect.ts): production's, outside every scope. */
export const controlPlaneTestApi: TestApi = apiOn(undefined);

const scopedApis = new WeakMap<Transport, TestApi>();

/**
 * The real client on `transport`, memoized per transport so a context default
 * keeps its identity across renders. The app's transport is controlPlaneTestApi
 * itself.
 */
export function testApiOf(transport: Transport): TestApi {
  if (transport === appTransport) return controlPlaneTestApi;
  let api = scopedApis.get(transport);
  if (!api) {
    api = apiOn(transport);
    scopedApis.set(transport, api);
  }
  return api;
}

const TestApiContext = createContext<TestApi | null>(null);

export function TestApiProvider({ api, children }: { api: TestApi; children: ReactNode }) {
  return <TestApiContext.Provider value={api}>{children}</TestApiContext.Provider>;
}

/** The provided api, else the real client on the nearest TransportScope's transport, else on the app's. */
export function useTestApi(): TestApi {
  const provided = useContext(TestApiContext);
  const transport = useTransport(appTransport);
  return provided ?? testApiOf(transport);
}
