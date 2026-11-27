import { createRootRoute } from '@tanstack/react-router'
import { describe, expect, it } from 'vitest'

import { createForgeConsoleRoutes, type ForgeConsoleComponents } from './forgeConsoleRoutes'
import { forgeConsoleSiteInsightsSearchSchema } from './routeSchemas'

const Placeholder = () => null
const LegacySpeed = () => null
const SiteInsights = () => null

// The slice of a TanStack route this test reads. The tree's own type is a
// union too deep to narrow by path, so the test reads it through this shape.
type RouteNode = {
  options: { path?: string; component?: unknown; validateSearch?: unknown }
  children?: RouteNode[]
}

function insightsRoutes(siteInsights?: ForgeConsoleComponents['siteInsights']) {
  const components = new Proxy({ siteInsights, speedInsights: LegacySpeed }, {
    get(target, key) {
      if (key === 'siteInsights' || key === 'speedInsights') return target[key]
      return Placeholder
    },
  }) as unknown as ForgeConsoleComponents
  const root = createRootRoute()
  const tree = createForgeConsoleRoutes(() => root, components) as unknown as RouteNode
  const consoleRoute = tree.children!.find((route) => route.options.path === '/forge/p/$project')!
  return consoleRoute.children!
}

describe('Speed insights route compatibility', () => {
  it('keeps the legacy page on its own route, with validated search', () => {
    const routes = insightsRoutes()
    const speed = routes.find((route) => route.options.path === 'speed-insights')!
    expect(speed.options.component).toBe(LegacySpeed)
    expect(speed.options.validateSearch).toBe(forgeConsoleSiteInsightsSearchSchema)
    for (const path of ['site-insights', 'site-insights/$view']) {
      expect(routes.find((route) => route.options.path === path)!.options.component).toBeUndefined()
    }
  })

  it('uses only the Site insights component for new views', () => {
    const routes = insightsRoutes(SiteInsights)
    for (const path of ['site-insights', 'site-insights/$view']) {
      expect(routes.find((route) => route.options.path === path)!.options.component).toBe(SiteInsights)
    }
    expect(routes.find((route) => route.options.path === 'speed-insights')!.options.component).toBe(LegacySpeed)
  })
})
