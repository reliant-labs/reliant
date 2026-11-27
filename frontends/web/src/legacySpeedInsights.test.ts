import { describe, expect, it } from 'vitest'
import { consolePath, pageOfTarget, speedInsightsPath, type ConsolePage } from './components/Forge/Console/paths'
import type { ForgeConsoleComponents } from './forgeConsoleRoutes'

describe('legacy Speed insights contract', () => {
  it('keeps the legacy page and target on the redirecting URL', () => {
    const page: ConsolePage = { kind: 'speed-insights' }
    expect(pageOfTarget(page)).toEqual(page)
    expect(consolePath('shop', page)).toBe('/forge/p/shop/speed-insights')
    expect(speedInsightsPath('shop')).toBe('/forge/p/shop/speed-insights')
    expect(consolePath('shop', { kind: 'site-insights' })).toBe('/forge/p/shop/site-insights')
  })

  it('accepts the legacy route-component property without the new property', () => {
    const legacy: Pick<ForgeConsoleComponents, 'speedInsights' | 'siteInsights'> = { speedInsights: () => null }
    expect(legacy.speedInsights).toBeTypeOf('function')
  })
})
