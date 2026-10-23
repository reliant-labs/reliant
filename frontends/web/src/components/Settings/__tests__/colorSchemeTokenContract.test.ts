import { describe, expect, it } from 'vitest'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

import { COLOR_SCHEMES, FORGE_COLOR_TOKENS } from '../../../lib/forgeTokens'

const __filename = fileURLToPath(import.meta.url)
const __dirname = path.dirname(__filename)

const THEMES_CSS_PATH = path.resolve(__dirname, '../../../themes/professional-themes.css')
const INDEX_CSS_PATH = path.resolve(__dirname, '../../../index.css')
const THEMES_CSS = fs.readFileSync(THEMES_CSS_PATH, 'utf-8')
const INDEX_CSS = fs.readFileSync(INDEX_CSS_PATH, 'utf-8')

function getThemeBlock(scheme: string, mode: 'light' | 'dark'): string {
  const selector = mode === 'light'
    ? `:root[data-color-scheme="${scheme}"]`
    : `.dark[data-color-scheme="${scheme}"]`

  const startIndex = THEMES_CSS.indexOf(selector)
  expect(startIndex, `Missing selector: ${selector}`).toBeGreaterThanOrEqual(0)

  const blockStart = THEMES_CSS.indexOf('{', startIndex)
  expect(blockStart, `Missing opening brace for: ${selector}`).toBeGreaterThanOrEqual(0)

  let depth = 0
  for (let i = blockStart; i < THEMES_CSS.length; i += 1) {
    const char = THEMES_CSS[i]
    if (char === '{') depth += 1
    if (char === '}') depth -= 1

    if (depth === 0) {
      return THEMES_CSS.slice(blockStart + 1, i)
    }
  }

  throw new Error(`Missing closing brace for: ${selector}`)
}

describe('professional color scheme token contract', () => {
  it.each(COLOR_SCHEMES)('defines config input surface tokens for %s in light and dark', (scheme) => {
    const lightBlock = getThemeBlock(scheme, 'light')
    const darkBlock = getThemeBlock(scheme, 'dark')

    expect(lightBlock).toContain('--config-input-bg:')
    expect(lightBlock).toContain('--config-input-border:')

    expect(darkBlock).toContain('--config-input-bg:')
    expect(darkBlock).toContain('--config-input-border:')
  })

  it('maps generic --input token to config-input-border for color-scheme surfaces', () => {
    expect(THEMES_CSS).toContain(':root[data-color-scheme]')
    expect(THEMES_CSS).toContain('--input: var(--config-input-border);')
  })

  it('does not keep legacy data-theme selectors in global CSS', () => {
    // Match a SELECTOR ([data-theme...]), not the bare string. The bare
    // match also caught the comment block that explains why reliant does
    // NOT use data-theme — forge's palette flips on it and reliant's flips
    // on `.dark` — so documenting the decision tripped the guard against
    // the decision. Strip comments first, then look for real usage.
    const withoutComments = INDEX_CSS.replace(/\/\*[\s\S]*?\*\//g, '')
    expect(withoutComments).not.toMatch(/\[data-theme/)
  })
})

// ── The forge token bridge, resolved per scheme ─────────────────────────
//
// forge's components paint with `--color-<token>` (src/lib/forgeTokens.ts).
// The bridge in index.css declares each one in terms of reliant variables
// (`hsl(var(--card))`), and THOSE come from three places: index.css's base
// `:root` / `.dark`, the scheme blocks in professional-themes.css, and the
// generic `:root[data-color-scheme]` block. A token is only bridged in a
// scheme if the whole chain bottoms out in a color there — a variable one
// scheme forgot is a transparent fill in that scheme alone, which is the
// failure a single-scheme check never sees.
//
// So this replays the cascade for <html> in every scheme × mode and resolves
// each token to a concrete `hsl(H S% L%)`. It resolves twice: at <html>, and
// inside `.forge-ui`, which redeclares the accent tokens (index.css, "THE
// ACCENT COLLISION"). That mirrors how custom properties actually compute: a
// var() is substituted where the property is DECLARED, and descendants
// inherit the substituted value.

interface CssBlock {
  selector: string
  /** Enclosing at-rules and selectors, outermost first. */
  parents: string[]
  decls: Map<string, string>
}

/** Every rule block in a stylesheet with its custom-property declarations. */
function parseBlocks(css: string): CssBlock[] {
  const text = css.replace(/\/\*[\s\S]*?\*\//g, '')
  const blocks: CssBlock[] = []
  const stack: CssBlock[] = []
  let segment = ''
  const flushDecls = () => {
    const top = stack[stack.length - 1]
    if (top) {
      for (const m of segment.matchAll(/(--[a-z0-9-]+)\s*:\s*([^;]+)/g)) top.decls.set(m[1], m[2].trim())
    }
  }
  for (const char of text) {
    if (char === '{') {
      // The selector is whatever follows the last declaration in the segment.
      const parts = segment.split(';')
      const selector = parts.pop()!.trim()
      segment = parts.join(';')
      flushDecls()
      const block: CssBlock = { selector, parents: stack.map((b) => b.selector), decls: new Map() }
      blocks.push(block)
      stack.push(block)
      segment = ''
    } else if (char === '}') {
      flushDecls()
      stack.pop()
      segment = ''
    } else if (char === ';') {
      flushDecls()
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
const unlayered = (b: CssBlock) => !layered(b)

/**
 * The custom properties in effect on <html> for one scheme and mode, in
 * cascade order (later wins): Tailwind's @theme (layer theme), index.css's
 * base `:root` then `.dark` (layer base), then the unlayered rules — index.css's
 * own `:root`, the scheme's light block, its dark block, and the generic
 * `[data-color-scheme]` block, which is last in its file.
 */
function htmlDecls(scheme: string, mode: 'light' | 'dark'): Map<string, string> {
  const layers = [
    declsOf(INDEX_BLOCKS, '@theme'),
    declsOf(INDEX_BLOCKS, ':root', layered),
    mode === 'dark' ? declsOf(INDEX_BLOCKS, '.dark', layered) : new Map<string, string>(),
    declsOf(INDEX_BLOCKS, ':root', unlayered),
    declsOf(THEME_BLOCKS, `:root[data-color-scheme="${scheme}"]`),
    mode === 'dark' ? declsOf(THEME_BLOCKS, `.dark[data-color-scheme="${scheme}"]`) : new Map<string, string>(),
    declsOf(THEME_BLOCKS, ':root[data-color-scheme]'),
  ]
  const merged = new Map<string, string>()
  for (const layer of layers) for (const [k, v] of layer) merged.set(k, v)
  return merged
}

/** Substitute every var() in `value`; null when one is undefined. */
function substitute(value: string, env: Map<string, string>, depth = 0): string | null {
  if (depth > 20) return null
  let out = value
  for (;;) {
    const m = /var\(\s*(--[a-z0-9-]+)\s*(?:,\s*([^()]*))?\)/.exec(out)
    if (!m) return out
    const raw = env.get(m[1]) ?? m[2]
    if (raw === undefined) return null
    const resolved = substitute(raw, env, depth + 1)
    if (resolved === null) return null
    out = out.slice(0, m.index) + resolved + out.slice(m.index + m[0].length)
  }
}

/** Every property in `env`, substituted where it is declared. */
function computeAll(env: Map<string, string>): Map<string, string | null> {
  const out = new Map<string, string | null>()
  for (const [k, v] of env) out.set(k, substitute(v, env))
  return out
}

const HSL_COLOR = /^hsl\(\s*\d+(\.\d+)?\s+\d+(\.\d+)?%\s+\d+(\.\d+)?%\s*(\/\s*(0|1|0?\.\d+))?\s*\)$/

const FORGE_SCOPE_DECLS = declsOf(INDEX_BLOCKS, '.forge-ui')

describe('forge token bridge in every color scheme', () => {
  it('declares every forge token in the @theme bridge', () => {
    const theme = declsOf(INDEX_BLOCKS, '@theme')
    const missing = FORGE_COLOR_TOKENS.filter((t) => !theme.has(`--color-${t}`))
    expect(missing, 'forge tokens with no `--color-<token>` in index.css @theme').toEqual([])
  })

  it('redeclares the whole accent family inside .forge-ui', () => {
    const accent = FORGE_COLOR_TOKENS.filter((t) => /(^|-)accent(-|$)/.test(t))
    expect(accent.length).toBeGreaterThanOrEqual(6)
    const missing = accent.filter((t) => !FORGE_SCOPE_DECLS.has(`--color-${t}`))
    expect(
      missing,
      'an accent token .forge-ui does not redeclare keeps resolving to reliant\'s muted --accent inside the scope',
    ).toEqual([])
  })

  const cases = COLOR_SCHEMES.flatMap((scheme) => (['light', 'dark'] as const).map((mode) => [scheme, mode] as const))

  it.each(cases)('resolves every forge token to a color in %s %s, outside and inside .forge-ui', (scheme, mode) => {
    const html = computeAll(htmlDecls(scheme, mode))
    // .forge-ui inherits <html>'s substituted values, then declares its own.
    const inherited = new Map<string, string>()
    for (const [k, v] of html) if (v !== null) inherited.set(k, v)
    for (const [k, v] of FORGE_SCOPE_DECLS) inherited.set(k, v)
    const scoped = computeAll(inherited)

    const broken: string[] = []
    for (const token of FORGE_COLOR_TOKENS) {
      for (const [where, env] of [['<html>', html], ['.forge-ui', scoped]] as const) {
        const value = env.get(`--color-${token}`)
        if (!value || !HSL_COLOR.test(value)) broken.push(`${token} at ${where}: ${value ?? 'unresolved'}`)
      }
    }
    expect(broken, `forge tokens that do not resolve to a color in ${scheme} ${mode}`).toEqual([])
  })

  it('gives .forge-ui the primary action color as accent, and leaves reliant\'s accent alone outside it', () => {
    for (const [scheme, mode] of cases) {
      const html = computeAll(htmlDecls(scheme, mode))
      const inherited = new Map<string, string>()
      for (const [k, v] of html) if (v !== null) inherited.set(k, v)
      for (const [k, v] of FORGE_SCOPE_DECLS) inherited.set(k, v)
      const scoped = computeAll(inherited)

      expect(html.get('--color-accent'), `${scheme} ${mode} <html>`).toBe(`hsl(${html.get('--accent')})`)
      expect(scoped.get('--color-accent'), `${scheme} ${mode} .forge-ui`).toBe(`hsl(${html.get('--primary')})`)
    }
  })
})