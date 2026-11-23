/**
 * shopfront's releases one at a time: DeployService.GetRelease, what a
 * release's page reads (its header, its tabs). The same releases, commits
 * included, as services.ts's ListReleases, whose table this reads, so the
 * list, a release's header and its Tests tab (Tests/fixtureReleases.ts)
 * name one commit. `currentEnvironmentIds` are the environments whose
 * current promotion is the release (console.ts's GetStatus: prod, staging and the pr-12 preview).
 *
 * Fabricated; every time is relative to FIXED_NOW.
 */
import type { Fixtures } from '../rpc'
import { PROD_RELEASE, RELEASES, STAGING_RELEASE, releaseMessage } from './services'

const CURRENT: Record<string, string[]> = {
  [PROD_RELEASE]: ['env-shop-prod'],
  [STAGING_RELEASE]: ['env-shop-staging'],
  '20261117.090000-0badc0ffee00': ['env-shop-pr12'],
}

export const releasesFixtures: Fixtures = {
  'controlplane.v1.DeployService/GetRelease': (req) => {
    const release = req.project === 'shopfront' ? RELEASES.find((r) => r.version === req.version) : undefined
    return release
      ? { release: releaseMessage(release), currentEnvironmentIds: CURRENT[release.version] ?? [] }
      : { __error: { code: 'not_found', message: `no release ${String(req.version)} in ${String(req.project)}` } }
  },
}
