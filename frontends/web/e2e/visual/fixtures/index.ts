import type { Fixtures } from '../rpc'
import { baseFixtures } from './base'
import { billingFixtures } from './billing'
import { chatFixtures } from './chat'
import { consoleFixtures } from './console'
import { projectCatalogFixtures } from './projects'
import { servicesFixtures } from './services'
import { u6cFixtures } from './u6c'
import { withDashboardFixtures } from './dashboards'

/** Every fixture, later tables winning on a shared key. */
export const allFixtures: Fixtures = withDashboardFixtures({ ...baseFixtures, ...chatFixtures, ...consoleFixtures, ...billingFixtures, ...projectCatalogFixtures, ...servicesFixtures, ...u6cFixtures })
