import type { Fixture, Fixtures } from '../rpc'
import { baseFixtures } from './base'
import { billingFixtures } from './billing'
import { chatFixtures } from './chat'
import { consoleFixtures } from './console'
import { projectCatalogFixtures } from './projects'
import { u6cFixtures } from './u6c'

/** Every fixture, later tables winning on a shared key. */
export const allFixtures: Fixtures = { ...baseFixtures, ...chatFixtures, ...consoleFixtures, ...billingFixtures, ...projectCatalogFixtures, ...u6cFixtures }
