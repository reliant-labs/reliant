import type { Fixtures } from '../rpc'
import { baseFixtures } from './base'
import { billingFixtures } from './billing'
import { chatFixtures } from './chat'
import { consoleFixtures } from './console'
import { projectCatalogFixtures } from './projects'

/** Every fixture, later tables winning on a shared key. */
export const allFixtures: Fixtures = { ...baseFixtures, ...chatFixtures, ...consoleFixtures, ...billingFixtures, ...projectCatalogFixtures }
