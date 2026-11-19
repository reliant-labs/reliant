// @vitest-environment node
/**
 * `--bubble` is the user chat message's fill (`bg-bubble`; web-styling,
 * "Elevation"). It is a token of its own so the message never borrows
 * `bg-muted` (interaction state, which inverts between light and dark) or a
 * primary tint (blue means action).
 *
 * In every color scheme, light and dark, the bubble:
 *   - is declared (an undeclared token paints transparent);
 *   - differs from --muted, --card and --background, so it reads as its own
 *     surface and never as a hovered row or a card;
 *   - carries body text at WCAG AA (4.5:1) and muted text at AA too.
 */
import { describe, expect, it } from 'vitest'

import { COLOR_SCHEMES } from '../../../lib/forgeTokens'
import { contrast, themeAt, type Mode } from '../../../test/themeCascade'

const cases = COLOR_SCHEMES.flatMap((scheme) => (['light', 'dark'] as const).map((mode) => [scheme, mode] as [string, Mode]))

describe.each(cases)('--bubble (%s, %s)', (scheme, mode) => {
  const theme = themeAt(scheme, mode)
  const bubble = theme.triplet('bubble')

  it('is declared', () => {
    expect(bubble, '--bubble').not.toBeNull()
  })

  it('differs from muted, card and the page', () => {
    for (const other of ['muted', 'card', 'background']) {
      expect(theme.triplet(other), `--${other}`).not.toBeNull()
      expect(bubble, `--bubble equals --${other}`).not.toBe(theme.triplet(other))
    }
  })

  it('carries body and muted text at AA', () => {
    const fill = theme.rgb('bubble')!
    expect(contrast(theme.rgb('foreground')!, fill), 'foreground on bubble').toBeGreaterThanOrEqual(4.5)
    expect(contrast(theme.rgb('muted-foreground')!, fill), 'muted-foreground on bubble').toBeGreaterThanOrEqual(4.5)
  })
})
