/**
 * shopfront's Tests pages, for the visual harness and the hosted preview.
 *
 * The data is the Tests pages' ONE fixture (src/components/Forge/Tests/
 * fixtures.ts: main's merged unit/integration/e2e report, a 30-commit
 * coverage trend with two releases marked, four flaky tests, the last CI
 * run), not a second copy. Two ways in, because the client does not call the
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
 *                          default "report"), read when the page loads. It
 *                          also hands the Coverage page's source seam,
 *                          CoverageSourceProvider, the fixture GitHub:
 *                          `?source=<scenario>` (Tests/coverageFixtures.ts
 *                          CoverageSourceScenario; default "github") shows
 *                          each no-source state.
 */
import { createElement, useMemo, type ReactNode } from 'react'

import {
  COVERAGE_SOURCE_SCENARIOS,
  fixtureCoverageResponse,
  fixtureCoverageSource,
  fixtureFileCoverageResponse,
  type CoverageSourceScenario,
} from '../../../src/components/Forge/Tests/coverageFixtures'
import { fixtureSummaryWire, fixtureTestApi, TESTS_SCENARIOS, type TestsScenario } from '../../../src/components/Forge/Tests/fixtures'
import { CoverageSourceProvider } from '../../../src/services/coverageSource/CoverageSourceContext'
import { TestApiProvider } from '../../../src/services/controlPlane/tests/TestApiContext'
import type { CoverageTarget } from '../../../src/services/controlPlane/tests/types'
import type { Fixtures } from '../rpc'

/** The enum fields of the summary's messages, by field name: their proto3-JSON names, by number. */
const ENUM_NAMES: Record<string, Record<number, string>> = {
  source: { 0: 'TEST_RUN_SOURCE_UNSPECIFIED', 1: 'TEST_RUN_SOURCE_CI', 2: 'TEST_RUN_SOURCE_LOCAL', 3: 'TEST_RUN_SOURCE_ENV', 4: 'TEST_RUN_SOURCE_CONFIRM' },
  outcome: { 0: 'TEST_RUN_OUTCOME_UNSPECIFIED', 1: 'TEST_RUN_OUTCOME_PASSED', 2: 'TEST_RUN_OUTCOME_FAILED', 3: 'TEST_RUN_OUTCOME_UNDETERMINED' },
  ingestState: { 0: 'TEST_RUN_INGEST_STATE_UNSPECIFIED', 1: 'TEST_RUN_INGEST_STATE_PENDING', 2: 'TEST_RUN_INGEST_STATE_INGESTED', 3: 'TEST_RUN_INGEST_STATE_FAILED' },
  kind: { 0: 'TEST_COVERAGE_NODE_KIND_UNSPECIFIED', 1: 'TEST_COVERAGE_NODE_KIND_PACKAGE', 2: 'TEST_COVERAGE_NODE_KIND_DIRECTORY', 3: 'TEST_COVERAGE_NODE_KIND_FILE' },
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

/** A GetCoverage / GetFileCoverage request's oneof target, from its proto3 JSON. */
function targetOfJson(req: Record<string, unknown>): CoverageTarget {
  if (typeof req.releaseVersion === 'string') return { releaseVersion: req.releaseVersion }
  if (typeof req.environmentId === 'string') return { environmentId: req.environmentId }
  return { gitCommit: String(req.gitCommit ?? '') }
}

const optionalString = (value: unknown) => (typeof value === 'string' && value !== '' ? value : undefined)

export const testsFixtures: Fixtures = {
  'controlplane.v1.TestsService/GetTestSummary': () => toProtoJson(fixtureSummaryWire('report')) as Record<string, unknown>,
  'controlplane.v1.TestsService/GetCoverage': (req) =>
    toProtoJson(
      fixtureCoverageResponse({
        project: String(req.project ?? ''),
        target: targetOfJson(req),
        path: String(req.path ?? ''),
        lane: optionalString(req.lane),
        baseSha: optionalString(req.baseSha),
        changedOnly: req.changedOnly === true,
      }),
    ) as Record<string, unknown>,
  'controlplane.v1.TestsService/GetFileCoverage': (req) =>
    toProtoJson(
      fixtureFileCoverageResponse({ project: String(req.project ?? ''), target: targetOfJson(req), path: String(req.path ?? ''), baseSha: optionalString(req.baseSha) }),
    ) as Record<string, unknown>,
}

/** `?tests=<scenario>` on the page's URL; anything else is "report". */
export function testsScenarioOf(search: string): TestsScenario {
  const value = new URLSearchParams(search).get('tests')
  return TESTS_SCENARIOS.find((scenario) => scenario === value) ?? 'report'
}

/** `?source=<scenario>` on the page's URL; anything else is "github". */
export function coverageSourceScenarioOf(search: string): CoverageSourceScenario {
  const value = new URLSearchParams(search).get('source')
  return COVERAGE_SOURCE_SCENARIOS.find((scenario) => scenario === value) ?? 'github'
}

/** The fixture TestsService, and the fixture GitHub behind the Coverage page's source, for everything under it (harnesses only: production never renders it). */
export function PreviewTestsProvider({ children }: { children?: ReactNode }) {
  const search = typeof window === 'undefined' ? '' : window.location.search
  const scenario = testsScenarioOf(search)
  const sourceScenario = coverageSourceScenarioOf(search)
  const api = useMemo(() => fixtureTestApi(scenario), [scenario])
  const fetch = useMemo(() => fixtureCoverageSource(sourceScenario), [sourceScenario])
  return createElement(TestApiProvider, { api }, createElement(CoverageSourceProvider, { fetch }, children))
}
