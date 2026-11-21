import type { Fixture, Fixtures } from '../rpc'
import { baseFixtures } from './base'
import { billingFixtures } from './billing'
import { chatFixtures } from './chat'
import { consoleFixtures } from './console'
import { dataFixtures } from './data'
import { projectCatalogFixtures } from './projects'
import { servicesFixtures } from './services'
import { u6cFixtures } from './u6c'
import { overviewFixtures, overviewQueryPanel } from './overview'

const QUERY_PANEL = 'controlplane.v1.ObservabilityService/QueryPanel'
const u6cChain = u6cFixtures[QUERY_PANEL] as (req: Record<string, unknown>) => unknown

/**
 * ONE QueryPanel chain (the tables below each wrap it, and a spread keeps only
 * the last): each file answers its own panels and hands the rest down —
 * overview → u6c → services → console.
 */
const queryPanel = ((req: Record<string, unknown>) => overviewQueryPanel(req) ?? u6cChain(req)) as Fixture

/** Every fixture, later tables winning on a shared key. */
export const allFixtures: Fixtures = {
  ...baseFixtures,
  ...chatFixtures,
  ...consoleFixtures,
  ...billingFixtures,
  ...projectCatalogFixtures,
  ...servicesFixtures,
  ...u6cFixtures,
  ...overviewFixtures,
  ...dataFixtures,
  [QUERY_PANEL]: queryPanel,
}
