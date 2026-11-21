/**
 * The project catalog (ProjectService/ListProjectsWithApps) for the project
 * selector's surfaces: eight projects and one app with no code here, with
 * app chips of every link kind (SOURCE, NAME, NOT_HOSTED), a static site, a
 * shared project, machines with and without the code, and two pins (a
 * project and the app-only row) in the person's `sidebar.projects` setting.
 * Fabricated and neutral. Every timestamp is relative to FIXED_NOW.
 */
import type { Fixtures } from '../rpc'
import { baseFixtures, DAY, DOCS_PROJECT, HOUR, IDS, iso, MIN, PROJECT, USER } from './base'

const project = (id: string, name: string, lastActive: string, extra: Record<string, unknown> = {}) => ({
  id, userId: USER.id, name, path: `/home/alex/code/${name}`, isGitRepo: true, defaultBranch: 'main',
  createdAt: iso(30 * DAY), updatedAt: lastActive, lastActive, orgId: IDS.org, ...extra,
})

const hosted = (name: string, extra: Record<string, unknown> = {}) => ({
  name, createdAt: iso(40 * DAY), environments: ['prod', 'staging'], hasStaticSite: false, sources: [], warning: '', ...extra,
})

const repo = (id: string, name: string, remote: string) => ({ id, name, relativePath: '', remote })
const onCloud = [{ daemonId: IDS.cloudDaemon, daemonName: 'cloud-machine', installState: 'PROJECT_INSTALL_STATE_INSTALLED' }]
const onLaptop = [{ daemonId: IDS.localDaemon, daemonName: 'alex-macbook', installState: 'PROJECT_INSTALL_STATE_INSTALLED' }]

const p = (n: number) => `0000000${n}-0000-4000-8000-0000000000a${n}`


const CATALOG = {
  orgId: IDS.org,
  hostedAppsState: 'HOSTED_APPS_STATE_OK',
  projects: [
    {
      project: { ...PROJECT, orgId: IDS.org },
      repos: [repo('r-shop', 'shopfront', 'github.com/acme/shopfront')],
      apps: [
        { name: 'shopfront', repoId: 'r-shop', subpath: '', link: 'PROJECT_APP_LINK_SOURCE',
          hosted: hosted('shopfront', { lastDeployedAt: iso(2 * HOUR) }) },
        { name: 'shopfront-admin', repoId: 'r-shop', subpath: 'admin', link: 'PROJECT_APP_LINK_NOT_HOSTED' },
      ],
      checkouts: onCloud,
    },
    {
      project: { ...DOCS_PROJECT, orgId: IDS.org },
      repos: [repo('r-docs', 'docs-site', 'github.com/acme/docs-site')],
      apps: [{ name: 'docs', repoId: 'r-docs', subpath: '', link: 'PROJECT_APP_LINK_NAME',
        hosted: hosted('docs', { lastDeployedAt: iso(DAY), hasStaticSite: true, environments: ['prod'] }) }],
      checkouts: onLaptop,
    },
    {
      project: project(p(1), 'mobile-api', iso(20 * MIN)),
      repos: [repo('r-m', 'mobile-api', 'github.com/acme/mobile-api')],
      apps: [{ name: 'mobile-api', repoId: 'r-m', subpath: '', link: 'PROJECT_APP_LINK_SOURCE',
        hosted: hosted('mobile-api', { lastDeployedAt: iso(30 * MIN) }) }],
      checkouts: onCloud,
    },
    {
      project: project(p(2), 'infra', iso(5 * HOUR)),
      repos: [repo('r-i', 'infra', 'github.com/acme/infra')],
      apps: [],
      checkouts: onCloud,
    },
    {
      project: project(p(3), 'data-pipeline', iso(2 * DAY)),
      repos: [repo('r-d', 'data-pipeline', 'github.com/acme/data-pipeline')],
      apps: [{ name: 'pipeline', repoId: 'r-d', subpath: 'deploy', link: 'PROJECT_APP_LINK_NAME', hosted: hosted('pipeline') }],
      checkouts: [],
    },
    {
      project: project(p(4), 'design-system', iso(3 * DAY), { viewerAccess: true, userId: 'someone-else' }),
      repos: [{ id: 'r-ds', name: 'design-system', relativePath: '', remote: '' }],
      apps: [],
      checkouts: [],
    },
    {
      project: project(p(5), 'billing-service', iso(6 * DAY)),
      repos: [repo('r-b', 'billing-service', 'github.com/acme/billing-service'), repo('r-b2', 'billing-proto', 'github.com/acme/billing-proto')],
      apps: [{ name: 'billing', repoId: 'r-b', subpath: '', link: 'PROJECT_APP_LINK_SOURCE',
        hosted: hosted('billing', { lastDeployedAt: iso(4 * DAY) }) }],
      checkouts: onLaptop,
    },
    {
      project: project(p(6), 'notebooks', iso(12 * DAY), { isGitRepo: false }),
      repos: [],
      apps: [],
      checkouts: onLaptop,
    },
  ],
  unlinkedApps: [
    hosted('status-page', { lastDeployedAt: iso(8 * HOUR), hasStaticSite: true, environments: ['prod'],
      sources: [{ remote: 'github.com/acme/status-page', subpath: '', commit: '4f1c2ab', lastSeenAt: iso(8 * HOUR) }] }),
  ],
}

export const projectCatalogFixtures: Fixtures = {
  'reliant.v1.ProjectService/ListProjectsWithApps': CATALOG,
}

/** The person's settings with two pins: shopfront and the app-only status-page. */
const PINS = { [IDS.org]: { pinned: [IDS.project, 'app:status-page'] } }
const baseSettings = baseFixtures['reliant.v1.SettingsService/ListSettings'] as { settings: object[]; total: number }

/** For the selector's surfaces only: the pins also move the sidebar, which other surfaces must not see. */
export const selectorSurfaceFixtures: Fixtures = {
  'reliant.v1.SettingsService/ListSettings': {
    settings: [
      ...baseSettings.settings,
      { id: 'sidebar.projects', key: 'sidebar.projects', value: JSON.stringify(PINS), valueType: 'string', createdAt: iso(DAY), updatedAt: iso(DAY) },
    ],
    total: baseSettings.total + 1,
  },
}
