import type { Fixture, Fixtures } from '../rpc'
import { baseFixtures } from './base'
import { billingFixtures } from './billing'
import { chatFixtures } from './chat'
import { consoleFixtures } from './console'
import { dataFixtures } from './data'
import { projectCatalogFixtures } from './projects'
import { servicesFixtures } from './services'
import { speedInsightsFixtures, speedInsightsQueryPanel } from './speedInsights'
import { u6cFixtures } from './u6c'
import { testsFixtures } from './tests'
import { releasesFixtures } from './releases'
import { overviewFixtures, overviewQueryPanel } from './overview'
import { withDashboardFixtures } from './dashboards'

const QUERY_PANEL = 'controlplane.v1.ObservabilityService/QueryPanel'
const u6cChain = u6cFixtures[QUERY_PANEL] as (req: Record<string, unknown>) => unknown

/**
 * ONE QueryPanel chain (the tables below each wrap it, and a spread keeps only
 * the last): each file answers its own panels and hands the rest down —
 * dashboards' gallery (withDashboardFixtures) → overview → speed insights →
 * u6c → services → console.
 */
const queryPanel = ((req: Record<string, unknown>) => overviewQueryPanel(req) ?? speedInsightsQueryPanel(req) ?? u6cChain(req)) as Fixture

/** Every fixture, later tables winning on a shared key. */
export const allFixtures: Fixtures = withDashboardFixtures({
  ...baseFixtures,
  ...chatFixtures,
  ...consoleFixtures,
  ...billingFixtures,
  ...projectCatalogFixtures,
  ...servicesFixtures,
  ...u6cFixtures,
  ...testsFixtures,
  ...releasesFixtures,
  ...overviewFixtures,
  ...speedInsightsFixtures,
  ...dataFixtures,
  [QUERY_PANEL]: queryPanel,
})
