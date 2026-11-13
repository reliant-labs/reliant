// @vitest-environment node
/**
 * The forge console's control edge meets WCAG 1.4.11 (3:1 non-text contrast)
 * against the page and the card in every color scheme, light and dark, and
 * its text ramp (ink-muted, ink-subtle) meets AA on every surface.
 *
 * The bug this pins: the console's filters, toggles and selects were drawn
 * with the structural border (1.3-1.4:1) or forge's `border-strong` (--input,
 * 1.45-1.75:1), the same faint frame as the charts beneath them, so a toolbar
 * read as one more row of the chart. `.forge-console` (src/index.css) redraws
 * `border-strong` as muted ink at a fixed alpha; this reads that alpha and
 * every scheme's tokens straight from the CSS, so a later token edit that
 * regresses fails here. Its vocabulary is components/Forge/controlStyles.ts.
 */
import { describe, expect, it } from 'vitest'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

import { COLOR_SCHEMES } from '../../../lib/forgeTokens'

const __dirname = path.dirname(fileURLToPath(import.meta.url))
const INDEX_CSS = fs.readFileSync(path.resolve(__dirname, '../../../index.css'), 'utf-8')
const THEMES_CSS = fs.readFileSync(path.resolve(__dirname, '../../../themes/professional-themes.css'), 'utf-8')

type Hsl = [number, number, number]
type Rgb = [number, number, number]

/** The surfaces a console control sits on: the page and a card. */
const SURFACES = ['background', 'card'] as const

/** The body of the first `selector {` block at or after `from`. */
function block(css: string, selector: string, from = 0): string {
  const start = css.indexOf(selector, from)
  expect(start, `missing ${selector}`).toBeGreaterThanOrEqual(0)
  const open = css.indexOf('{', start)
  let depth = 0
  for (let i = open; i < css.length; i += 1) {
    if (css[i] === '{') depth += 1
    if (css[i] === '}') depth -= 1
    if (depth === 0) return css.slice(open + 1, i)
  }
  throw new Error(`unclosed ${selector}`)
}

/** `--name: H S% L%` in a block, following one `var(--other)` indirection; null when undeclared. */
function hsl(body: string, name: string): Hsl | null {
  const match = body.match(new RegExp(`--${name}:\\s*([^;]+);`))
  if (!match) return null
  const value = match[1]!.trim()
  const ref = value.match(/^var\(--([\w-]+)\)$/)
  if (ref) return hsl(body, ref[1]!)
  const [h, s, l] = value.split(/\s+/).map((part) => parseFloat(part))
  return [h!, s!, l!]
}

function toRgb([h, s, l]: Hsl): Rgb {
  const sat = s / 100
  const light = l / 100
  const k = (n: number) => (n + h / 30) % 12
  const a = sat * Math.min(light, 1 - light)
  const f = (n: number) => light - a * Math.max(-1, Math.min(k(n) - 3, Math.min(9 - k(n), 1)))
  return [f(0) * 255, f(8) * 255, f(4) * 255]
}

/** `fg` at `alpha` composited over `bg`. */
function over(fg: Rgb, alpha: number, bg: Rgb): Rgb {
  return [0, 1, 2].map((i) => fg[i]! * alpha + bg[i]! * (1 - alpha)) as Rgb
}

function luminance(rgb: Rgb): number {
  const [r, g, b] = rgb.map((v) => {
    const c = v / 255
    return c <= 0.03928 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4
  })
  return 0.2126 * r! + 0.7152 * g! + 0.0722 * b!
}

function contrast(a: Rgb, b: Rgb): number {
  const [x, y] = [luminance(a), luminance(b)].sort((p, q) => q - p)
  return (x! + 0.05) / (y! + 0.05)
}

// index.css: the `:root` / `.dark` blocks that carry the semantic tokens.
const semanticsAt = INDEX_CSS.indexOf('--success:')
const lightBase = block(INDEX_CSS, ':root', INDEX_CSS.lastIndexOf(':root', semanticsAt))
const darkBase = block(INDEX_CSS, '.dark', semanticsAt)

/** [name, block] for the base theme and every color scheme in one mode. */
function themes(mode: 'light' | 'dark'): Array<[string, string]> {
  const out: Array<[string, string]> = [['base', mode === 'light' ? lightBase : darkBase]]
  const prefix = mode === 'light' ? ':root[data-color-scheme="' : '.dark[data-color-scheme="'
  for (let at = THEMES_CSS.indexOf(prefix); at >= 0; at = THEMES_CSS.indexOf(prefix, at + 1)) {
    const scheme = THEMES_CSS.slice(at + prefix.length, THEMES_CSS.indexOf('"', at + prefix.length))
    out.push([scheme, block(THEMES_CSS, prefix + scheme, at)])
  }
  return out
}

/** The console scope's control edge: muted ink at a fixed alpha. */
function consoleEdgeAlpha(): number {
  const scope = block(INDEX_CSS, '.forge-console {')
  const match = scope.match(/--color-border-strong:\s*hsl\(var\(--muted-foreground\)\s*\/\s*([\d.]+)\)/)
  expect(match, '.forge-console must draw --color-border-strong as hsl(var(--muted-foreground) / <alpha>)').not.toBeNull()
  return parseFloat(match![1]!)
}

/** `fg` mixed with `other` in gamma-encoded sRGB, as CSS `color-mix(in srgb, fg P%, other)` does. */
function mix(fg: Rgb, share: number, other: Rgb): Rgb {
  return [0, 1, 2].map((i) => fg[i]! * share + other[i]! * (1 - share)) as Rgb
}

/** The console scope's muted ink: --muted-foreground mixed toward --foreground. Returns the muted share. */
function consoleMutedShare(): number {
  const scope = block(INDEX_CSS, '.forge-console {')
  const match = scope.match(/--color-ink-muted:\s*color-mix\(in srgb,\s*hsl\(var\(--muted-foreground\)\)\s*([\d.]+)%,\s*hsl\(var\(--foreground\)\)\)/)
  expect(match, '.forge-console must draw --color-ink-muted as color-mix(in srgb, hsl(var(--muted-foreground)) <P>%, hsl(var(--foreground)))').not.toBeNull()
  return parseFloat(match![1]!) / 100
}

/** Text surfaces: a control or a row sits on the page or a card; a menu or tooltip on the popover. */
const TEXT_SURFACES = ['background', 'card', 'popover'] as const

describe.each(['light', 'dark'] as const)('console control edge (%s)', (mode) => {
  const all = themes(mode)
  const alpha = consoleEdgeAlpha()

  it('covers the base theme and every color scheme', () => {
    expect(all).toHaveLength(1 + COLOR_SCHEMES.length)
  })

  it.each(all)('%s: a control edge is 3:1 against every surface it sits on', (_name, body) => {
    const ink = hsl(body, 'muted-foreground')
    expect(ink, '--muted-foreground not declared').not.toBeNull()
    for (const surface of SURFACES) {
      const fill = hsl(body, surface)
      if (!fill) continue
      const edge = over(toRgb(ink!), alpha, toRgb(fill))
      // 3:1 is WCAG 1.4.11's floor; the console keeps a margin over it (it read as faint at 3.3:1).
      expect(contrast(edge, toRgb(fill)), `control edge on --${surface}`).toBeGreaterThanOrEqual(4)
    }
  })

  it.each(all)('%s: the selected segment and a set filter (primary ring and ink) stand out from the track', (_name, body) => {
    const primary = hsl(body, 'primary')
    const primaryInk = hsl(body, 'primary-ink')
    expect(primary, '--primary not declared').not.toBeNull()
    for (const surface of SURFACES) {
      const fill = hsl(body, surface)
      if (!fill) continue
      expect(contrast(toRgb(primary!), toRgb(fill)), `selected ring on --${surface}`).toBeGreaterThanOrEqual(3)
      if (primaryInk) expect(contrast(toRgb(primaryInk), toRgb(fill)), `selected label on --${surface}`).toBeGreaterThanOrEqual(4.5)
    }
  })
})

/**
 * The console's text ramp: placeholders, inactive segments, column headers
 * and metadata lines are `ink-muted`; captions and icons `ink-subtle`. Both
 * are text, so both clear AA on every surface, and the ramp keeps its order:
 * ink above ink-muted above ink-subtle.
 */
describe.each(['light', 'dark'] as const)('console text ramp (%s)', (mode) => {
  const all = themes(mode)
  const share = consoleMutedShare()

  it('subtle is the plain muted foreground', () => {
    expect(block(INDEX_CSS, '.forge-console {')).toMatch(/--color-ink-subtle:\s*hsl\(var\(--muted-foreground\)\);/)
  })

  it.each(all)('%s: muted and subtle text are AA, in order, on every surface', (_name, body) => {
    const ink = toRgb(hsl(body, 'foreground')!)
    const subtle = toRgb(hsl(body, 'muted-foreground')!)
    const muted = mix(subtle, share, ink)
    for (const surface of TEXT_SURFACES) {
      const fill = hsl(body, surface)
      if (!fill) continue
      const bg = toRgb(fill)
      // Secondary metadata and placeholders read at 12-13px over these panels: a margin over AA.
      expect(contrast(muted, bg), `ink-muted on --${surface}`).toBeGreaterThanOrEqual(6)
      expect(contrast(subtle, bg), `ink-subtle on --${surface}`).toBeGreaterThanOrEqual(4.5)
      expect(contrast(muted, bg), `ink-muted above ink-subtle on --${surface}`).toBeGreaterThan(contrast(subtle, bg))
      expect(contrast(ink, bg), `ink above ink-muted on --${surface}`).toBeGreaterThan(contrast(muted, bg) * 1.5)
    }
  })
})
