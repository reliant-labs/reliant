// @vitest-environment node
/**
 * The two line tokens (web-styling, "Borders"):
 *
 *   --border         a region's edge or an object's edge (card, input,
 *                    composer, menu)
 *   --border-subtle  rows inside a list or table, a tool fold's indent rule,
 *                    chart gridlines. Nothing else
 *
 * In every color scheme, light and dark, the subtle rule is declared, is
 * still a visible line on the card and on the page, and is always quieter
 * than --border, so the two never trade places.
 */
import { describe, expect, it } from 'vitest'

import { COLOR_SCHEMES } from '../../../lib/forgeTokens'
import { contrast, themeAt, type Mode } from '../../../test/themeCascade'

const cases = COLOR_SCHEMES.flatMap((scheme) => (['light', 'dark'] as const).map((mode) => [scheme, mode] as [string, Mode]))
const SURFACES = ['card', 'background'] as const

describe.each(cases)('line tokens (%s, %s)', (scheme, mode) => {
  const theme = themeAt(scheme, mode)

  it('declares --border and --border-subtle', () => {
    expect(theme.triplet('border'), '--border').not.toBeNull()
    expect(theme.triplet('border-subtle'), '--border-subtle').not.toBeNull()
  })

  it.each(SURFACES)('--border-subtle is a visible line on --%s, quieter than --border', (surface) => {
    const fill = theme.rgb(surface)!
    const subtle = contrast(theme.rgb('border-subtle')!, fill)
    expect(subtle, `--border-subtle on --${surface}`).toBeGreaterThan(1.04)
    expect(subtle, `--border-subtle on --${surface} vs --border`).toBeLessThan(contrast(theme.rgb('border')!, fill))
  })
})
