/**
 * shopfront's Security pages, for the visual harness and the hosted preview.
 *
 * The data is reliant#3085's ONE fixture (src/components/Forge/Security/
 * fixtures.ts, "findings": prod and staging scanned, a KEV-listed critical,
 * fixable and unfixable rows, an exemption, SBOMs per artifact), not a second
 * copy. It cannot ride the RPC table like the other files here: #3085's
 * client does not call the network until control-plane's VulnerabilityService
 * is re-vendored (services/controlPlane/vulnerabilities/vulnerabilityApi.ts),
 * so a harness installs this API instead, before the app boots:
 *
 *   globalThis.__vulnerabilityApi = securityHarnessApi()
 *
 * which VulnerabilityApiContext reads as its default (harness only; production
 * never sets it). The fixture is written for a project "shop" with
 * environments env-prod / env-staging / env-dev on releases v1.4.0 / v1.5.0;
 * this adapter renames those to shopfront's (console.ts, services.ts) in both
 * directions, so the scope picker and release links agree with the rest of
 * the preview.
 *
 * Once the client calls the network, replace this with an RPC fixture table
 * ('controlplane.v1.VulnerabilityService/<Rpc>') like the other files.
 */
import { fixtureVulnerabilityApi, type VulnScenario } from '../../../src/components/Forge/Security/fixtures'
import type { VulnerabilityApi } from '../../../src/services/controlPlane/vulnerabilities/VulnerabilityApiContext'

/** The fixture's names → shopfront's. Exact string matches only, wherever they appear. */
const TO_SHOPFRONT: Record<string, string> = {
  'env-prod': 'env-shop-prod',
  'env-staging': 'env-shop-staging',
  'env-dev': 'env-shop-pr12',
  dev: 'pr-12',
  'v1.4.0': '20261118.121200-a1b2c3d4e5f6',
  'v1.5.0': '20261118.144000-f6e5d4c3b2a1',
}
const TO_FIXTURE: Record<string, string> = Object.fromEntries(Object.entries(TO_SHOPFRONT).map(([a, b]) => [b, a]))

function rename<T>(value: T, names: Record<string, string>): T {
  if (typeof value === 'string') return (names[value] ?? value) as T
  if (Array.isArray(value)) return value.map((item) => rename(item, names)) as T
  if (value && typeof value === 'object' && Object.getPrototypeOf(value) === Object.prototype) {
    return Object.fromEntries(Object.entries(value).map(([key, item]) => [key, rename(item, names)])) as T
  }
  return value
}

export function securityHarnessApi(scenario: VulnScenario = 'findings'): VulnerabilityApi {
  const api = fixtureVulnerabilityApi(scenario)
  const wrap =
    <K extends keyof VulnerabilityApi>(method: K) =>
    (async (req: unknown, opts?: unknown) => {
      const call = api[method] as (req: unknown, opts?: unknown) => Promise<unknown>
      return rename(await call(rename(req, TO_FIXTURE), opts), TO_SHOPFRONT)
    }) as VulnerabilityApi[K]
  return {
    getVulnerabilitySummary: wrap('getVulnerabilitySummary'),
    listVulnerabilities: wrap('listVulnerabilities'),
    getVulnerability: wrap('getVulnerability'),
    getReleaseVulnerabilities: wrap('getReleaseVulnerabilities'),
  }
}
