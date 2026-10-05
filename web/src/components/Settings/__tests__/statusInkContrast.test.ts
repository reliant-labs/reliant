/**
 * Status text meets WCAG AA (4.5:1) on every scheme's page and card, in both
 * modes — including on the 10-12% tint of its own fill that badges sit on.
 *
 * The bug this pins: `--color-warning-ink` / `--color-success-ink` were the
 * raw FILL colours, so amber text on a light card was ~2:1 ("Shell" badge,
 * "Needs attention", "Paused"), and white text on the dark-mode success fill
 * (the Inbox Approve button) was 2.2:1. Read straight from the CSS, so a later
 * token edit that regresses contrast fails here rather than in a QA pass.
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

/** `--name: H S% L%` in a block, following one `var(--other)` indirection. */
function hsl(body: string, name: string): Hsl {
  const match = body.match(new RegExp(`--${name}:\\s*([^;]+);`))
  expect(match, `--${name} not declared`).not.toBeNull()
  const value = match![1]!.trim()
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

const tint = (fill: Rgb, under: Rgb, alpha: number): Rgb =>
  fill.map((v, i) => v * alpha + under[i]! * (1 - alpha)) as Rgb

// index.css: the first `:root {` / `.dark {` inside the base layer carry the
// semantic tokens (the earlier top-level `:root` is the accent bridge).
const semanticsAt = INDEX_CSS.indexOf('--success:')
const lightBase = block(INDEX_CSS, ':root', INDEX_CSS.lastIndexOf(':root', semanticsAt))
const darkBase = block(INDEX_CSS, '.dark', semanticsAt)

/** Every surface status text can sit on: the base and each scheme's page and card. */
function surfaces(mode: 'light' | 'dark'): Array<[string, Rgb]> {
  const base = mode === 'light' ? lightBase : darkBase
  const out: Array<[string, Rgb]> = [
    ['base background', toRgb(hsl(base, 'background'))],
    ['base card', toRgb(hsl(base, 'card'))],
  ]
  const prefix = mode === 'light' ? ':root[data-color-scheme="' : '.dark[data-color-scheme="'
  for (let at = THEMES_CSS.indexOf(prefix); at >= 0; at = THEMES_CSS.indexOf(prefix, at + 1)) {
    const scheme = THEMES_CSS.slice(at + prefix.length, THEMES_CSS.indexOf('"', at + prefix.length))
    const body = block(THEMES_CSS, prefix + scheme, at)
    if (/--background:/.test(body)) out.push([`${scheme} background`, toRgb(hsl(body, 'background'))])
    if (/--card:/.test(body)) out.push([`${scheme} card`, toRgb(hsl(body, 'card'))])
  }
  return out
}

describe.each(['light', 'dark'] as const)('status ink contrast (%s)', (mode) => {
  const base = mode === 'light' ? lightBase : darkBase

  it.each(['warning', 'success', 'destructive'])('%s-ink is AA on every surface and on its own badge tint', (kind) => {
    const ink = toRgb(hsl(base, `${kind}-ink`))
    const fill = toRgb(hsl(base, kind))
    for (const [name, surface] of surfaces(mode)) {
      expect(contrast(ink, surface), `${kind}-ink on ${name}`).toBeGreaterThanOrEqual(4.5)
      expect(contrast(ink, tint(fill, surface, 0.12)), `${kind}-ink on ${kind} tint over ${name}`).toBeGreaterThanOrEqual(4.5)
    }
  })

  it('success-foreground is AA on the success fill (the Approve button)', () => {
    expect(contrast(toRgb(hsl(base, 'success-foreground')), toRgb(hsl(base, 'success')))).toBeGreaterThanOrEqual(4.5)
  })
})

describe('status ink is what the utilities resolve to', () => {
  it('maps text-*-ink onto the ink tokens, not the fills', () => {
    expect(INDEX_CSS).toContain('--color-warning-ink: hsl(var(--warning-ink));')
    expect(INDEX_CSS).toContain('--color-success-ink: hsl(var(--success-ink));')
    expect(INDEX_CSS).toContain('--color-danger-ink: hsl(var(--destructive-ink));')
  })
})
