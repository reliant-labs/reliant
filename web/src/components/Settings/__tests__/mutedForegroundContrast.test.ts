/**
 * Muted text (`text-muted-foreground`) meets WCAG AA (4.5:1) on every surface
 * it is drawn on, in every color scheme, light and dark.
 *
 * The bug this pins: light schemes set --muted-foreground at 45-47%
 * lightness, which measured 4.42:1 on a white card (research/
 * WORKFLOW_EDITOR_UX_REVIEW.md §1 issue 10) and fell to 3.7:1 on the
 * modern-teal and forest wells. Muted text is most of the workflow editor's
 * secondary copy (hints, captions, section labels), so the fix is the token,
 * not a darker class at each call site.
 *
 * "Every surface" is the page, a card, a popover/modal, and the two tinted
 * fills muted text sits on inside them: --muted/--accent (hover and selected
 * rows) and --config-input-bg (the config panel's input wells). Read straight
 * from the CSS, so a later token edit that regresses fails here.
 */
import { describe, expect, it } from 'vitest'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const __dirname = path.dirname(fileURLToPath(import.meta.url))
const INDEX_CSS = fs.readFileSync(path.resolve(__dirname, '../../../index.css'), 'utf-8')
const THEMES_CSS = fs.readFileSync(path.resolve(__dirname, '../../../themes/professional-themes.css'), 'utf-8')

type Hsl = [number, number, number]
type Rgb = [number, number, number]

const SURFACES = ['background', 'card', 'popover', 'surface-modal', 'muted', 'accent', 'config-input-bg'] as const

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

describe.each(['light', 'dark'] as const)('muted-foreground contrast (%s)', (mode) => {
  const all = themes(mode)

  it('covers the base theme and all ten color schemes', () => {
    expect(all).toHaveLength(11)
  })

  it.each(all)('%s: muted text is AA on every surface it sits on', (_name, body) => {
    const ink = hsl(body, 'muted-foreground')
    expect(ink, '--muted-foreground not declared').not.toBeNull()
    for (const surface of SURFACES) {
      const fill = hsl(body, surface)
      if (!fill) continue
      expect(contrast(toRgb(ink!), toRgb(fill)), `muted-foreground on --${surface}`).toBeGreaterThanOrEqual(4.5)
    }
  })
})
