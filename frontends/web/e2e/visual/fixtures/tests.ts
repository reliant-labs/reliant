/**
 * shopfront's Tests pages, for the visual harness and the hosted preview.
 *
 * The data is the Tests pages' ONE fixture (src/components/Forge/Tests/
 * fixtures.ts: main's merged unit/integration/e2e report, a 30-commit
 * coverage trend with two releases marked, four flaky tests, the last CI
 * run; runFlakeFixtures.ts: nine runs, a failed branch run, ten flaky tests
 * and TestSyncJob's record), not a second copy. Two ways in, because the client does not call the
 * network until control-plane's TestsService is re-vendored (W0;
 * services/controlPlane/tests/testApi.ts):
 *
 *   testsFixtures          the RPC table ('controlplane.v1.TestsService/<Rpc>'),
 *                          proto3 JSON like every other file here, registered in
 *                          index.ts. It answers once the client calls the network.
 *   PreviewTestsProvider   the fixture api handed to the pages' own seam,
 *                          TestApiProvider, at the root (as security.ts does):
 *                          what powers the preview today. `?tests=<scenario>`
 *                          picks the scenario (Tests/fixtures.ts TestsScenario;
 *                          default "report"), read when the page loads.
 */
import { createElement, useMemo, type ReactNode } from 'react'

import { fixtureSummaryWire, fixtureTestApi, TESTS_SCENARIOS, type TestsScenario } from '../../../src/components/Forge/Tests/fixtures'
import { fixtureFlakyWire, fixtureRunsWire, fixtureRunWire, fixtureTestWire } from '../../../src/components/Forge/Tests/runFlakeFixtures'
import { TestApiProvider } from '../../../src/services/controlPlane/tests/TestApiContext'
import type { Fixtures } from '../rpc'

/** The enum fields of TestsService's messages, by field name: their proto3-JSON names, by number. */
const ENUM_NAMES: Record<string, Record<number, string>> = {
  source: { 0: 'TEST_RUN_SOURCE_UNSPECIFIED', 1: 'TEST_RUN_SOURCE_CI', 2: 'TEST_RUN_SOURCE_LOCAL', 3: 'TEST_RUN_SOURCE_ENV', 4: 'TEST_RUN_SOURCE_CONFIRM' },
  outcome: { 0: 'TEST_RUN_OUTCOME_UNSPECIFIED', 1: 'TEST_RUN_OUTCOME_PASSED', 2: 'TEST_RUN_OUTCOME_FAILED', 3: 'TEST_RUN_OUTCOME_UNDETERMINED' },
  ingestState: { 0: 'TEST_RUN_INGEST_STATE_UNSPECIFIED', 1: 'TEST_RUN_INGEST_STATE_PENDING', 2: 'TEST_RUN_INGEST_STATE_INGESTED', 3: 'TEST_RUN_INGEST_STATE_FAILED' },
  status: { 0: 'TEST_CASE_STATUS_UNSPECIFIED', 1: 'TEST_CASE_STATUS_PASS', 2: 'TEST_CASE_STATUS_FAIL', 3: 'TEST_CASE_STATUS_SKIP' },
  state: {
    0: 'TEST_FLAKE_STATE_UNSPECIFIED',
    1: 'TEST_FLAKE_STATE_HEALTHY',
    2: 'TEST_FLAKE_STATE_SUSPECTED',
    3: 'TEST_FLAKE_STATE_FLAKY',
    4: 'TEST_FLAKE_STATE_QUARANTINED',
    5: 'TEST_FLAKE_STATE_FIXED',
    6: 'TEST_FLAKE_STATE_REMOVED',
  },
  signal: { 0: 'TEST_FLAKE_SIGNAL_UNSPECIFIED', 1: 'TEST_FLAKE_SIGNAL_SAME_TREE', 2: 'TEST_FLAKE_SIGNAL_RETRY_PASS', 3: 'TEST_FLAKE_SIGNAL_CONFIRM', 4: 'TEST_FLAKE_SIGNAL_HISTORY' },
  signals: { 0: 'TEST_FLAKE_SIGNAL_UNSPECIFIED', 1: 'TEST_FLAKE_SIGNAL_SAME_TREE', 2: 'TEST_FLAKE_SIGNAL_RETRY_PASS', 3: 'TEST_FLAKE_SIGNAL_CONFIRM', 4: 'TEST_FLAKE_SIGNAL_HISTORY' },
  verdict: {
    0: 'TEST_FLAKE_VERDICT_UNSPECIFIED',
    1: 'TEST_FLAKE_VERDICT_CONFIRMED',
    2: 'TEST_FLAKE_VERDICT_REAL',
    3: 'TEST_FLAKE_VERDICT_RACED',
    4: 'TEST_FLAKE_VERDICT_INCONCLUSIVE',
    5: 'TEST_FLAKE_VERDICT_UNATTRIBUTED',
    6: 'TEST_FLAKE_VERDICT_HOST',
    7: 'TEST_FLAKE_VERDICT_CORRELATED',
  },
}

const isTimestamp = (value: object): value is { seconds: bigint; nanos: number } =>
  'seconds' in value && typeof (value as { seconds: unknown }).seconds === 'bigint' && 'nanos' in value

/** A wire message as proto3 JSON: timestamps as RFC 3339, int64 as strings, enums by name, unset fields left out. */
export function toProtoJson(value: unknown, field = ''): unknown {
  if (typeof value === 'bigint') return value.toString()
  if (typeof value === 'number' && ENUM_NAMES[field]) return ENUM_NAMES[field]![value] ?? value
  if (Array.isArray(value)) return value.map((item) => toProtoJson(item, field))
  if (value && typeof value === 'object') {
    if (isTimestamp(value)) return new Date(Number(value.seconds) * 1000 + Math.floor(value.nanos / 1e6)).toISOString()
    return Object.fromEntries(
      Object.entries(value)
        .filter(([key, item]) => key !== '$typeName' && item !== undefined)
        .map(([key, item]) => [key, toProtoJson(item, key)]),
    )
  }
  return value
}

const PROJECT = { orgId: null, project: 'shopfront' }
const str = (value: unknown) => (typeof value === 'string' ? value : '')

/** The report scenario's answers, read from the request's proto3-JSON fields (the filters the table can match on). */
export const testsFixtures: Fixtures = {
  'controlplane.v1.TestsService/GetTestSummary': () => toProtoJson(fixtureSummaryWire('report')) as Record<string, unknown>,
  'controlplane.v1.TestsService/ListTestRuns': (req) =>
    toProtoJson(fixtureRunsWire('report', { ...PROJECT, filters: { branch: str(req.branch) || undefined, lane: str(req.lane) || undefined }, pageToken: str(req.pageToken) })) as Record<string, unknown>,
  'controlplane.v1.TestsService/GetTestRun': (req) => toProtoJson(fixtureRunWire('report', { ...PROJECT, runId: str(req.runId) })) as Record<string, unknown>,
  'controlplane.v1.TestsService/ListFlakyTests': () => toProtoJson(fixtureFlakyWire('report', { ...PROJECT, filters: {} })) as Record<string, unknown>,
  'controlplane.v1.TestsService/GetTest': (req) => toProtoJson(fixtureTestWire('report', { ...PROJECT, testId: str(req.testId) })) as Record<string, unknown>,
}

/** `?tests=<scenario>` on the page's URL; anything else is "report". */
export function testsScenarioOf(search: string): TestsScenario {
  const value = new URLSearchParams(search).get('tests')
  return TESTS_SCENARIOS.find((scenario) => scenario === value) ?? 'report'
}

/** The fixture TestsService for everything under it (harnesses only: production never renders it). */
export function PreviewTestsProvider({ children }: { children?: ReactNode }) {
  const scenario = testsScenarioOf(typeof window === 'undefined' ? '' : window.location.search)
  const api = useMemo(() => fixtureTestApi(scenario), [scenario])
  return createElement(TestApiProvider, { api, children })
}
