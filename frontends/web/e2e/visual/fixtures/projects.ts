/**
 * The project catalog (ProjectService/ListProjectsWithApps) for the project
 * selector's surfaces: eight projects and one app with no code here, with
 * app chips of every link kind (SOURCE, NAME, NOT_HOSTED), a static site, a
 * shared project, machines with and without the code, and two pins (a
 * project and the app-only row) in the person's `sidebar.projects` setting.
 * Fabricated and neutral. Every timestamp is relative to FIXED_NOW.
 *
 * One catalog for every surface: the shared table answers it to all of them
 * (the selector opens from any page). The Projects page and a project's page
 * read it with the selector's pins, plus a second app with no code here
 * (`ledger-worker`, cut from a subdirectory of another repo), so their
 * "Apps without code here" section has two rows.
 *
 * shopfront records where it was deployed from (a code source), so the app
 * console's Code card has a source to show. `appCodeCardFixtures` is the same
 * catalog for the app-code-card surface alone: shopfront deployed from two
 * repositories (the D5 warning), also held by the person's fork
 * (`shopfront-experiments`, same name) and a teammate's project shared with
 * the org.
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

/** A project with no app (`infra`): a project's own page, which a deployed project's id redirects past. */
export const NO_APP_PROJECT_ID = p(2)

/** The chat filter's three projects in its surfaces: shopfront, docs-site and mobile-api. */
export const FILTER_PROJECT_IDS = [IDS.project, IDS.docsProject, p(1)]


const SHOPFRONT_SOURCE = { remote: 'github.com/acme/shopfront', subpath: '', commit: 'a1b2c3d4e5f6a7b8c9d0', lastSeenAt: iso(3 * HOUR) }
const SHOPFRONT = hosted('shopfront', { lastDeployedAt: iso(2 * HOUR), sources: [SHOPFRONT_SOURCE] })

const CATALOG = {
  orgId: IDS.org,
  hostedAppsState: 'HOSTED_APPS_STATE_OK',
  projects: [
    {
      project: { ...PROJECT, orgId: IDS.org },
      repos: [repo('r-shop', 'shopfront', 'github.com/acme/shopfront')],
      apps: [
        { name: 'shopfront', repoId: 'r-shop', subpath: '', link: 'PROJECT_APP_LINK_SOURCE', hosted: SHOPFRONT },
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

/** shopfront deployed from two repositories, held by the person's fork and a teammate's shared project. */
const TWO_REPOSITORIES = hosted('shopfront', {
  lastDeployedAt: iso(2 * HOUR),
  sources: [SHOPFRONT_SOURCE, { remote: 'github.com/alex/shopfront', subpath: 'apps/web', commit: '9f8e7d6c5b4a39281706', lastSeenAt: iso(2 * DAY) }],
  warning:
    'shopfront is deployed from two repositories: github.com/acme/shopfront and github.com/alex/shopfront. The next deploy from either replaces what the other shipped.',
})
const withTwoRepositories = <T extends { name: string }>(app: T) => (app.name === 'shopfront' && 'hosted' in app ? { ...app, hosted: TWO_REPOSITORIES } : app)

/** For the app-code-card surface only: other surfaces keep the plain catalog. */
export const appCodeCardFixtures: Fixtures = {
  'reliant.v1.ProjectService/ListProjectsWithApps': {
    ...CATALOG,
    projects: [
      ...CATALOG.projects.map((row) => ({ ...row, apps: row.apps.map(withTwoRepositories) })),
      {
        project: project(p(8), 'shopfront-experiments', iso(2 * DAY), { remoteUrl: 'https://github.com/alex/shopfront.git' }),
        repos: [repo('r-exp', 'shopfront', 'github.com/alex/shopfront')],
        apps: [{ name: 'shopfront', repoId: 'r-exp', subpath: 'apps/web', link: 'PROJECT_APP_LINK_NAME', hosted: TWO_REPOSITORIES }],
        checkouts: onCloud,
      },
      {
        project: project(p(7), 'shopfront', iso(DAY), { viewerAccess: true, userId: 'user-sam', path: '' }),
        repos: [{ id: 'r-sam', name: 'shopfront', relativePath: '', remote: '' }],
        apps: [{ name: 'shopfront', repoId: 'r-sam', subpath: '', link: 'PROJECT_APP_LINK_NAME', hosted: TWO_REPOSITORIES }],
        checkouts: [],
      },
    ],
  },
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

/** A second app with no code here, for the Projects page's own section. */
const LEDGER_WORKER = hosted('ledger-worker', {
  lastDeployedAt: iso(4 * DAY),
  sources: [{ remote: 'github.com/acme/ledger', subpath: 'worker', commit: 'd00d123', lastSeenAt: iso(4 * DAY) }],
})

/** For the Projects page and a project's page: the selector's pins, and the catalog with LEDGER_WORKER. */
export const projectsPageSurfaceFixtures: Fixtures = {
  ...selectorSurfaceFixtures,
  'reliant.v1.ProjectService/ListProjectsWithApps': { ...CATALOG, unlinkedApps: [...CATALOG.unlinkedApps, LEDGER_WORKER] },
}
