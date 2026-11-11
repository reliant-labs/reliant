// @vitest-environment node
/**
 * Accent TEXT (`text-primary-ink`, `--primary-ink`) meets WCAG AA (4.5:1) on
 * every surface it is drawn on, in every color scheme, light and dark.
 *
 * Why a token of its own: `--primary` is a FILL. It is tuned so a label on it
 * (`--primary-foreground`) is legible, which says nothing about the fill used
 * as text on a page. The brand blue (#0a70e0, a white-label fill at 4.8:1) is
 * 3.9:1 as text on a dark panel, so `text-primary` could not carry the brand
 * without failing AA (research/app-ux-review-2026-11/00-brand-and-shell.md).
 * Text now paints in --primary-ink, and `text-primary` is not used at all
 * (the guard at the bottom).
 *
 * "Every surface" is what accent text sits on: the page, a card, a popover or
 * modal, the hover/selected fills (--muted, --accent, --secondary), the config
 * input wells, and the primary tints badges and selected rows use
 * (`bg-primary/10`..`/20` over the page and a card). Resolved through the
 * whole cascade (test/themeCascade.ts), so a scheme that forgets the token
 * falls back to index.css's base value and is checked as such.
 */
import { describe, expect, it } from 'vitest'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

import { COLOR_SCHEMES } from '../../../lib/forgeTokens'
import { contrast, over, themeAt, type Mode, type Rgb } from '../../../test/themeCascade'

const here = path.dirname(fileURLToPath(import.meta.url))
const SURFACES = ['background', 'card', 'popover', 'surface-modal', 'muted', 'accent', 'secondary', 'config-input-bg']
const TINTS = [0.1, 0.15, 0.2]
const cases = COLOR_SCHEMES.flatMap((scheme) => (['light', 'dark'] as const).map((mode) => [scheme, mode] as [string, Mode]))

function failures(scheme: string, mode: Mode): string[] {
  const theme = themeAt(scheme, mode)
  const ink = theme.rgb('primary-ink')
  if (!ink) return ['--primary-ink is not declared']
  const out: string[] = []
  const check = (name: string, fill: Rgb) => {
    const ratio = contrast(ink, fill)
    if (ratio < 4.5) out.push(`${name}: ${ratio.toFixed(2)}:1`)
  }
  for (const surface of SURFACES) {
    const fill = theme.rgb(surface)
    if (fill) check(`--${surface}`, fill)
  }
  const primary = theme.rgb('primary')!
  for (const under of ['background', 'card']) {
    for (const alpha of TINTS) check(`primary/${alpha * 100} over --${under}`, over(primary, theme.rgb(under)!, alpha))
  }
  return out
}

describe('primary-ink contrast', () => {
  it.each(cases)('%s %s: accent text is AA on every surface it sits on', (scheme, mode) => {
    expect(failures(scheme, mode), `--primary-ink below 4.5:1 in ${scheme} ${mode}`).toEqual([])
  })

  it('maps the text-primary-ink utility and forge accent-ink onto the ink token, not the fill', () => {
    const config = fs.readFileSync(path.resolve(here, '../../../../tailwind.config.js'), 'utf-8')
    expect(config).toContain('ink: "hsl(var(--primary-ink) / <alpha-value>)"')
    const css = fs.readFileSync(path.resolve(here, '../../../index.css'), 'utf-8')
    expect(css).toContain('--color-accent-ink: hsl(var(--primary-ink));')
    expect(css).not.toMatch(/--color-accent-ink: hsl\(var\(--primary\)\)/)
  })
})

const SRC = path.resolve(here, '../../..')

/** Every .ts/.tsx/.css file under src, skipping generated code. */
function sources(dir: string): string[] {
  const out: string[] = []
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    const full = path.join(dir, entry.name)
    if (entry.isDirectory()) {
      if (entry.name !== 'gen' && entry.name !== 'node_modules') out.push(...sources(full))
    } else if (/\.(tsx?|css)$/.test(entry.name) && full !== fileURLToPath(import.meta.url)) {
      out.push(full)
    }
  }
  return out
}

describe('accent text uses the ink token', () => {
  it('never paints text with the primary FILL (text-primary, or color: hsl(var(--primary)))', () => {
    // `text-primary` alone, with a variant (`hover:`) or an alpha (`/80`),
    // but not `text-primary-foreground` / `text-primary-ink`.
    const utility = /(?<![\w-])text-primary(?![\w-])/
    const inline = /(?<![\w-])color:\s*['"`]?hsl\(var\(--primary\)/
    const offenders: string[] = []
    for (const file of sources(SRC)) {
      // A test's fixture data may name the fill as a color value (a thread's
      // dot color); only a utility class there would be a real text paint.
      const isTest = /__tests__|\.test\./.test(file)
      fs.readFileSync(file, 'utf-8').split('\n').forEach((line, i) => {
        if (utility.test(line) || (!isTest && inline.test(line))) offenders.push(`${path.relative(SRC, file)}:${i + 1}`)
      })
    }
    expect(offenders, 'use text-primary-ink (or hsl(var(--primary-ink))) for accent text').toEqual([])
  })
})
