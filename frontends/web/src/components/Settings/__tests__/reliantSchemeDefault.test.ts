/**
 * The Reliant scheme: the brand's tokens, the default for everyone, and the
 * elevation rule it (and every other scheme) must keep.
 *
 *   - The default is one value, DEFAULT_COLOR_SCHEME, read by settingsSync,
 *     useThemeInitialization and the Appearance picker. index.html's
 *     pre-hydration bootstrap cannot import it, so its copy (default, map and
 *     fallback) is held equal here: a drift is a flash of the wrong scheme on
 *     every cold load.
 *   - `black` (the old default's id) is retired onto Reliant, so everyone on
 *     the old default moves; Pure Black stays selectable as `pure-black`.
 *   - Reliant dark IS the site's palette (landing-page tailwind.config.js),
 *     within rounding of the HSL triplets.
 *   - Elevation: in dark, page < card < popover < muted (muted is hover and
 *     selected only, and it must still read on a card); in light, muted sits
 *     below the card. The web-styling skill's bg-muted trap.
 *   - --brand-violet, the label hue, is AA on every surface in every scheme.
 */
import { describe, expect, it } from 'vitest'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

import {
  COLOR_SCHEMES,
  COLOR_SCHEME_ATTR,
  DEFAULT_COLOR_SCHEME,
  DEFAULT_COLOR_SCHEME_ATTR,
  RETIRED_COLOR_SCHEME_IDS,
} from '../../../lib/forgeTokens'
import { contrast, themeAt, type Mode, type Rgb } from '../../../test/themeCascade'

const here = path.dirname(fileURLToPath(import.meta.url))
const WEB = path.resolve(here, '../../../..')
const read = (rel: string) => fs.readFileSync(path.join(WEB, rel), 'utf-8')
const cases = COLOR_SCHEMES.flatMap((scheme) => (['light', 'dark'] as const).map((mode) => [scheme, mode] as [string, Mode]))

describe('Reliant is the default for everyone', () => {
  it('names reliant as the default id and attribute', () => {
    expect(DEFAULT_COLOR_SCHEME).toBe('reliant')
    expect(DEFAULT_COLOR_SCHEME_ATTR).toBe('reliant')
    expect(COLOR_SCHEME_ATTR[DEFAULT_COLOR_SCHEME]).toBe(DEFAULT_COLOR_SCHEME_ATTR)
    expect(COLOR_SCHEMES).toContain(DEFAULT_COLOR_SCHEME_ATTR)
  })

  it('moves the old default (`black`) onto Reliant and keeps Pure Black selectable', () => {
    expect(COLOR_SCHEME_ATTR.black).toBe('reliant')
    expect(RETIRED_COLOR_SCHEME_IDS.black).toBe('reliant')
    expect(COLOR_SCHEME_ATTR['pure-black']).toBe('pure-black')
  })

  it('maps every retired id to a live one', () => {
    for (const [retired, now] of Object.entries(RETIRED_COLOR_SCHEME_IDS)) {
      expect(COLOR_SCHEME_ATTR[retired], retired).toBe(COLOR_SCHEME_ATTR[now])
      expect(RETIRED_COLOR_SCHEME_IDS[now], `${now} is itself retired`).toBeUndefined()
    }
  })

  it('reads the default from one constant in every TypeScript read site', () => {
    for (const file of ['src/services/settingsSync.ts', 'src/hooks/useThemeInitialization.ts', 'src/components/Settings/ColorSchemeSelector.tsx']) {
      const body = read(file)
      expect(body, file).toContain('DEFAULT_COLOR_SCHEME')
      expect(body, `${file} hard-codes a scheme default`).not.toMatch(/COLOR_SCHEME,\s*["']black["']|\|\|\s*["']pure-black["']|\?\?\s*["']pure-black["']/)
    }
  })

  it("keeps index.html's bootstrap copy equal to forgeTokens", () => {
    const html = read('index.html')
    expect(html).toContain(`localStorage.getItem('appearance.colorScheme') || '${DEFAULT_COLOR_SCHEME}'`)
    expect(html).toContain(`schemeMap[savedScheme] || '${DEFAULT_COLOR_SCHEME_ATTR}'`)
    expect(html).toContain(`setAttribute('data-color-scheme', '${DEFAULT_COLOR_SCHEME_ATTR}');`)
    const literal = /var schemeMap = (\{[\s\S]*?\});/.exec(html)
    expect(literal, 'index.html schemeMap').not.toBeNull()
    const map = JSON.parse(literal![1]!.replace(/'/g, '"')) as Record<string, string>
    expect(map).toEqual(COLOR_SCHEME_ATTR)
  })
})

/** sRGB 0..255 from #rrggbb. */
const hex = (value: string): Rgb => [1, 3, 5].map((i) => parseInt(value.slice(i, i + 2), 16)) as Rgb

describe('Reliant dark is the site palette', () => {
  const theme = themeAt('reliant', 'dark')
  it.each([
    ['background', '#0a0a0f'], // canvas
    ['card', '#111118'], // panel
    ['popover', '#18181f'], // panel.raised
    ['foreground', '#f4f4f5'], // fg
    ['muted-foreground', '#9d9da6'], // fg.muted
    ['primary', '#0a70e0'], // brand.blue.600, the white-label fill
    ['primary-hover', '#0961c4'], // brand.blue.700
    ['ring', '#0d8cff'], // brand.blue
    ['primary-ink', '#5cb0ff'], // brand.blue.300
  ])('--%s is %s', (token, expected) => {
    const got = theme.rgb(token)!
    const want = hex(expected)
    got.forEach((channel, i) => expect(Math.abs(channel - want[i]!), `${token} channel ${i}`).toBeLessThanOrEqual(2))
  })

})

describe('Reliant fills carry their labels at AA', () => {
  // Light is derived, not imported from the site, so it is held to the same
  // bar as dark: every fill that carries a label, at rest and on hover.
  it.each(['light', 'dark'] as const)('%s: white label on the blue fill, at rest, on hover and on the active tab', (mode) => {
    const theme = themeAt('reliant', mode)
    const low: string[] = []
    for (const [label, fill] of [
      ['primary-foreground', 'primary'],
      ['primary-foreground', 'primary-hover'],
      ['tab-active-foreground', 'tab-active'],
    ] as const) {
      const ratio = contrast(theme.rgb(label)!, theme.rgb(fill)!)
      if (ratio < 4.5) low.push(`${label} on ${fill}: ${ratio.toFixed(2)}:1`)
    }
    expect(low).toEqual([])
  })
})

describe('elevation in every scheme', () => {
  const lum = (rgb: Rgb) => rgb.reduce((a, c) => a + c, 0)
  it.each(cases)('%s %s: background recesses, muted is hover-only', (scheme, mode) => {
    const theme = themeAt(scheme, mode)
    const [page, card, popover, muted] = ['background', 'card', 'popover', 'muted'].map((t) => lum(theme.rgb(t)!))
    if (mode === 'dark') {
      expect(page, 'page below card').toBeLessThan(card!)
      expect(card, 'card below popover').toBeLessThan(popover!)
      expect(card, 'muted above card (the inversion rule)').toBeLessThan(muted!)
    } else {
      expect(page, 'page below card').toBeLessThan(card!)
      expect(muted, 'muted below card').toBeLessThan(card!)
    }
  })
})

describe('brand violet', () => {
  it.each(cases)('%s %s: --brand-violet is AA on every surface', (scheme, mode) => {
    const theme = themeAt(scheme, mode)
    const violet = theme.rgb('brand-violet')!
    const low: string[] = []
    for (const surface of ['background', 'card', 'popover', 'muted']) {
      const ratio = contrast(violet, theme.rgb(surface)!)
      if (ratio < 4.5) low.push(`${surface}: ${ratio.toFixed(2)}:1`)
    }
    expect(low).toEqual([])
  })
})
