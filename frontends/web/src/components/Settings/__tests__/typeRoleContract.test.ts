// @vitest-environment node
/**
 * The type roles (web-styling skill, "Type roles"): text is sized by what it
 * IS — `text-meta`, `text-ui`, `text-copy`, `text-code`, `text-section`,
 * `text-title`, `text-figure` — not by a Tailwind step.
 *
 *   - Each role has the size, line-height, weight and tracking the restyle
 *     direction set, in rem (so the Appearance size still scales it), at the
 *     px it names at the default 15px root.
 *   - The ladder holds: content is never smaller than the chrome beside it,
 *     and nothing reads below 12px (`text-meta` is the floor).
 *   - `cn()` treats every role as a font size. tailwind-merge reads an
 *     unknown `text-<word>` as a color, so without lib/typeRoles.ts
 *     `cn("text-muted-foreground", "text-meta")` silently drops the color.
 *   - The radius ladder the roles pair with: control `rounded-md`, container
 *     `rounded-xl`, hero `rounded-2xl`, on `--radius: 0.625rem` (restyle v1:
 *     softer, still restrained).
 *   - `text-2xs`/`text-3xs` are for badges and chart axes only
 *     (src/styles/stylingContract.json `tinyText.allow`). The call sites
 *     that predate the roles are a ratchet (`tinyText.legacy`): a count may
 *     go down, never up, and no new file may join it.
 */
import { describe, expect, it } from 'vitest'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

import { DEFAULT_FONT_SIZE, FONT_SIZE_MAP } from '../../../lib/rootFontSize'
import { TYPE_ROLES } from '../../../lib/typeRoles'
import { cn } from '../../../lib/utils'

const here = path.dirname(fileURLToPath(import.meta.url))
const WEB = path.resolve(here, '../../../..')
const SRC = path.join(WEB, 'src')
const CONFIG = fs.readFileSync(path.join(WEB, 'tailwind.config.js'), 'utf-8')
const CONTRACT = JSON.parse(fs.readFileSync(path.join(SRC, 'styles/stylingContract.json'), 'utf-8')) as {
  tinyText: { allow: string[]; legacy: Record<string, number> }
}

interface Step {
  size: number
  lineHeight: number
  fontWeight?: string
  letterSpacing?: string
}

/** The `fontSize` scale of tailwind.config.js, read as text (the config is loaded by Tailwind, not importable here). */
function fontSizes(): Map<string, Step> {
  const body = CONFIG.slice(CONFIG.indexOf('fontSize: {'))
  const steps = new Map<string, Step>()
  for (const m of body.matchAll(/^\s+"?([\w-]+)"?:\s*\["([\d.]+)rem",\s*\{([^}]*)\}\]/gm)) {
    const opts = Object.fromEntries([...m[3]!.matchAll(/(\w+):\s*"([^"]+)"/g)].map((o) => [o[1]!, o[2]!]))
    steps.set(m[1]!, {
      size: parseFloat(m[2]!),
      lineHeight: parseFloat(opts.lineHeight!),
      fontWeight: opts.fontWeight,
      letterSpacing: opts.letterSpacing,
    })
  }
  return steps
}

const ROOT_PX = parseFloat(FONT_SIZE_MAP[DEFAULT_FONT_SIZE]!)
const px = (rem: number) => Math.round(rem * ROOT_PX * 10) / 10

// [role, size px, line-height px, weight, tracking] at the default 15px root.
const ROLES: Array<[string, number, number, string | undefined, string | undefined]> = [
  ['meta', 12.2, 16, undefined, undefined],
  ['ui', 14.1, 20, undefined, undefined],
  ['copy', 16, 26, undefined, undefined],
  ['code', 13, 20, undefined, undefined],
  ['section', 16, 24, '600', undefined],
  ['title', 24, 32, '600', '-0.02em'],
  ['figure', 32, 40, '600', '-0.03em'],
]

describe('type roles', () => {
  const steps = fontSizes()

  it('reads the default root as 15px', () => {
    expect(ROOT_PX).toBe(15)
  })

  it.each(ROLES)('text-%s is %spx / %spx', (role, size, lineHeight, weight, tracking) => {
    const step = steps.get(role)
    expect(step, `tailwind.config.js has no fontSize.${role}`).toBeDefined()
    expect(px(step!.size)).toBeCloseTo(size, 0)
    expect(px(step!.lineHeight)).toBeCloseTo(lineHeight, 0)
    expect(step!.fontWeight).toBe(weight)
    expect(step!.letterSpacing).toBe(tracking)
  })

  it('lists exactly the configured roles for tailwind-merge', () => {
    expect([...TYPE_ROLES].sort()).toEqual(ROLES.map(([role]) => role).sort())
  })

  it('keeps the ladder: meta is the floor, content outranks chrome, one title, one figure', () => {
    const size = (role: string) => px(steps.get(role)!.size)
    expect(size('meta')).toBeGreaterThanOrEqual(12)
    expect(size('meta')).toBeLessThan(size('code'))
    expect(size('code')).toBeLessThan(size('ui'))
    expect(size('ui')).toBeLessThan(size('copy'))
    expect(size('section')).toBeGreaterThanOrEqual(size('copy'))
    expect(size('title')).toBeGreaterThan(size('section'))
    expect(size('figure')).toBeGreaterThan(size('title'))
  })

  it('merges roles as font sizes in cn()', () => {
    expect(cn('text-muted-foreground', 'text-meta')).toBe('text-muted-foreground text-meta')
    expect(cn('text-sm', 'text-ui')).toBe('text-ui')
    for (const role of TYPE_ROLES) expect(cn('text-xs', `text-${role}`), role).toBe(`text-${role}`)
  })
})

describe('radius ladder', () => {
  it('keeps --radius at 0.625rem and leaves xl / 2xl at Tailwind’s defaults', () => {
    expect(fs.readFileSync(path.join(SRC, 'index.css'), 'utf-8')).toMatch(/--radius:\s*0\.625rem;/)
    const radius = CONFIG.slice(CONFIG.indexOf('borderRadius: {'), CONFIG.indexOf('}', CONFIG.indexOf('borderRadius: {')))
    expect(radius).toContain('md: "calc(var(--radius) - 2px)"')
    expect(radius).not.toMatch(/\bxl:|"2xl"/)
  })
})

describe('text-2xs / text-3xs', () => {
  const allow = CONTRACT.tinyText.allow.map((glob) => {
    let body = ''
    for (let i = 0; i < glob.length; i += 1) {
      if (glob.startsWith('**/', i)) {
        body += '(?:.*/)?'
        i += 2
      } else if (glob.startsWith('**', i)) {
        body += '.*'
        i += 1
      } else {
        body += glob[i] === '*' ? '[^/]*' : glob[i]!.replace(/[.+?^${}()|[\]\\]/g, '\\$&')
      }
    }
    return new RegExp(`^${body}$`)
  })

  function sources(dir: string, out: string[] = []): string[] {
    for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
      const full = path.join(dir, entry.name)
      if (entry.isDirectory()) {
        if (entry.name !== 'gen' && entry.name !== '__tests__') sources(full, out)
      } else if (/\.tsx?$/.test(entry.name) && !/\.(test|spec)\.tsx?$/.test(entry.name)) {
        out.push(full)
      }
    }
    return out
  }

  it('appear outside badges and chart axes no more than the legacy ratchet allows', () => {
    const over: string[] = []
    for (const file of sources(SRC)) {
      const rel = path.relative(SRC, file).split(path.sep).join('/')
      if (allow.some((re) => re.test(rel))) continue
      const count = (fs.readFileSync(file, 'utf-8').match(/\btext-(?:2xs|3xs)\b/g) ?? []).length
      const legacy = CONTRACT.tinyText.legacy[rel] ?? 0
      if (count > legacy) over.push(`${rel}: ${count} (ratchet ${legacy})`)
    }
    expect(over, 'text-2xs/3xs in content: use text-meta (src/styles/stylingContract.json)').toEqual([])
  })

  it('lists only legacy files that still exist', () => {
    const missing = Object.keys(CONTRACT.tinyText.legacy).filter((rel) => !fs.existsSync(path.join(SRC, rel)))
    expect(missing).toEqual([])
  })
})
