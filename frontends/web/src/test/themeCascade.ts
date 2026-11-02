// Copyright (c) 2025 Reliant Labs

/**
 * Replays the theme cascade for <html> in one color scheme and mode, straight
 * from the stylesheets, so a contrast test checks the colors the browser will
 * actually compute rather than one block read in isolation.
 *
 * Cascade order (later wins): Tailwind's @theme, index.css's base `:root` then
 * `.dark` (both in a layer), the unlayered `:root` rules, the scheme's light
 * block, its dark block, and the generic `:root[data-color-scheme]` block.
 * colorSchemeTokenContract.test.ts walks the same order for the forge bridge.
 */
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const here = path.dirname(fileURLToPath(import.meta.url))
export const INDEX_CSS = fs.readFileSync(path.resolve(here, '../index.css'), 'utf-8')
export const THEMES_CSS = fs.readFileSync(path.resolve(here, '../themes/professional-themes.css'), 'utf-8')

export type Mode = 'light' | 'dark'
export type Rgb = [number, number, number]

interface CssBlock {
  selector: string
  parents: string[]
  decls: Map<string, string>
}

function parseBlocks(css: string): CssBlock[] {
  const text = css.replace(/\/\*[\s\S]*?\*\//g, '')
  const blocks: CssBlock[] = []
  const stack: CssBlock[] = []
  let segment = ''
  const flush = () => {
    const top = stack[stack.length - 1]
    if (top) for (const m of segment.matchAll(/(--[a-z0-9-]+)\s*:\s*([^;]+)/g)) top.decls.set(m[1]!, m[2]!.trim())
  }
  for (const char of text) {
    if (char === '{') {
      const parts = segment.split(';')
      const selector = parts.pop()!.trim()
      segment = parts.join(';')
      flush()
      const block: CssBlock = { selector, parents: stack.map((b) => b.selector), decls: new Map() }
      blocks.push(block)
      stack.push(block)
      segment = ''
    } else if (char === '}') {
      flush()
      stack.pop()
      segment = ''
    } else if (char === ';') {
      flush()
      segment = ''
    } else {
      segment += char
    }
  }
  return blocks
}

const INDEX_BLOCKS = parseBlocks(INDEX_CSS)
const THEME_BLOCKS = parseBlocks(THEMES_CSS)

function declsOf(blocks: CssBlock[], selector: string, where: (b: CssBlock) => boolean = () => true) {
  const merged = new Map<string, string>()
  for (const block of blocks) {
    if (block.selector === selector && where(block)) for (const [k, v] of block.decls) merged.set(k, v)
  }
  return merged
}

const layered = (b: CssBlock) => b.parents.some((p) => p.startsWith('@layer'))

/** The custom properties declared for <html> in one scheme and mode, unsubstituted. */
function htmlDecls(scheme: string, mode: Mode): Map<string, string> {
  const layers = [
    declsOf(INDEX_BLOCKS, '@theme'),
    declsOf(INDEX_BLOCKS, ':root', layered),
    mode === 'dark' ? declsOf(INDEX_BLOCKS, '.dark', layered) : new Map<string, string>(),
    declsOf(INDEX_BLOCKS, ':root', (b) => !layered(b)),
    declsOf(THEME_BLOCKS, `:root[data-color-scheme="${scheme}"]`),
    mode === 'dark' ? declsOf(THEME_BLOCKS, `.dark[data-color-scheme="${scheme}"]`) : new Map<string, string>(),
    declsOf(THEME_BLOCKS, ':root[data-color-scheme]'),
  ]
  const merged = new Map<string, string>()
  for (const layer of layers) for (const [k, v] of layer) merged.set(k, v)
  return merged
}

function substitute(value: string, env: Map<string, string>, depth = 0): string | null {
  if (depth > 20) return null
  let out = value
  for (;;) {
    const m = /var\(\s*(--[a-z0-9-]+)\s*(?:,\s*([^()]*))?\)/.exec(out)
    if (!m) return out
    const raw = env.get(m[1]!) ?? m[2]
    if (raw === undefined) return null
    const resolved = substitute(raw, env, depth + 1)
    if (resolved === null) return null
    out = out.slice(0, m.index) + resolved + out.slice(m.index + m[0].length)
  }
}

/**
 * A resolver for one scheme × mode: `triplet('card')` is the substituted
 * `H S% L%` of `--card`, or null when the cascade never declares it.
 */
export function themeAt(scheme: string, mode: Mode) {
  const env = htmlDecls(scheme, mode)
  return {
    triplet(name: string): string | null {
      const raw = env.get(`--${name}`)
      return raw === undefined ? null : substitute(raw, env)
    },
    rgb(name: string): Rgb | null {
      const value = this.triplet(name)
      return value === null ? null : tripletToRgb(value)
    },
  }
}

/** `H S% L%` (an HSL triplet, the format of every theme token) → sRGB 0..255. */
export function tripletToRgb(value: string): Rgb {
  const m = /^\s*([\d.]+)\s+([\d.]+)%\s+([\d.]+)%\s*$/.exec(value)
  if (!m) throw new Error(`not an HSL triplet: ${value}`)
  const [h, s, l] = [Number(m[1]), Number(m[2]) / 100, Number(m[3]) / 100]
  const k = (n: number) => (n + h / 30) % 12
  const a = s * Math.min(l, 1 - l)
  const f = (n: number) => l - a * Math.max(-1, Math.min(k(n) - 3, 9 - k(n), 1))
  return [f(0) * 255, f(8) * 255, f(4) * 255]
}

/** `fill` at `alpha` composited over `under`. */
export const over = (fill: Rgb, under: Rgb, alpha: number): Rgb =>
  fill.map((v, i) => v * alpha + under[i]! * (1 - alpha)) as Rgb

function luminance(rgb: Rgb): number {
  const [r, g, b] = rgb.map((v) => {
    const c = v / 255
    return c <= 0.04045 ? c / 12.92 : ((c + 0.055) / 1.055) ** 2.4
  })
  return 0.2126 * r! + 0.7152 * g! + 0.0722 * b!
}

/** WCAG 2 contrast ratio. */
export function contrast(a: Rgb, b: Rgb): number {
  const [x, y] = [luminance(a), luminance(b)].sort((p, q) => q - p)
  return (x! + 0.05) / (y! + 0.05)
}
