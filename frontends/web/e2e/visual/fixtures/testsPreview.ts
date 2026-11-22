/**
 * The Tests pages' fixture seam for a preview build (harnesses only: production
 * never renders it). A preview's root wraps the app in PreviewTestsProvider:
 *
 *   ?tests=<scenario>    the scenario (Tests/fixtures.ts TestsScenario; default
 *                        "report"), read when the page loads
 *   ?source=<scenario>   the Coverage page's fixture GitHub
 *                        (Tests/coverageFixtures.ts CoverageSourceScenario;
 *                        default "github"), to show each no-source state
 *
 * Not in tests.ts: CoverageSourceContext reaches the app's transport, which
 * does not load in Node, where the visual harness imports the RPC table.
 */
import { createElement, useMemo, type ReactNode } from 'react'

import { COVERAGE_SOURCE_SCENARIOS, fixtureCoverageSource, type CoverageSourceScenario } from '../../../src/components/Forge/Tests/coverageFixtures'
import { fixtureTestApi, TESTS_SCENARIOS, type TestsScenario } from '../../../src/components/Forge/Tests/fixtures'
import { CoverageSourceProvider } from '../../../src/services/coverageSource/CoverageSourceContext'
import { TestApiProvider } from '../../../src/services/controlPlane/tests/TestApiContext'

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
  // children ride in props, not as createElement's third argument (TS2769 against the providers' required children).
  return createElement(TestApiProvider, { api, children: createElement(CoverageSourceProvider, { fetch, children }) })
}
