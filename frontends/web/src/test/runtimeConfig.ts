import { resetConfigForTests } from '@/lib/config_gen'

/**
 * Give this test a runtime config document, as config.js would.
 *
 * Replaces the whole document (keys not named take their config_gen.ts
 * defaults) and drops loadConfig()'s memo, so the next read sees it. A module
 * that reads the config at module scope (services/controlPlane/config.ts)
 * needs `vi.resetModules()` and a fresh import after this.
 *
 * setup.ts resets to an empty document before every test.
 */
export function setRuntimeConfig(values: Record<string, string>): void {
  window.__FORGE_CONFIG__ = { ...values }
  resetConfigForTests()
}
