// @vitest-environment node
/**
 * `--scrim` is the one backdrop behind dialogs, sheets and drawers
 * (`bg-scrim`). It is black at an alpha chosen per mode, because the same
 * alpha does different work on the two canvases: on a white page 40% black
 * dims the page itself, while on a near-black page no black alpha can move
 * the canvas, so dark mode has to dim the content harder.
 *
 * In every color scheme, light and dark, the scrim:
 *   - is declared (an undeclared token paints transparent: no dimming);
 *   - dims body text to at most 60% of its luminance, so an overlay visibly
 *     separates from the page in both modes.
 * And page-covering overlays use it rather than a raw `bg-black/NN`.
 */
import { describe, expect, it } from 'vitest'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

import { COLOR_SCHEMES } from '../../../lib/forgeTokens'
import { themeAt, type Mode } from '../../../test/themeCascade'

const SRC = path.resolve(path.dirname(fileURLToPath(import.meta.url)), '../../..')

const cases = COLOR_SCHEMES.flatMap((scheme) => (['light', 'dark'] as const).map((mode) => [scheme, mode] as [string, Mode]))

/** `0 0% 0% / 0.4` → its alpha. */
function scrimAlpha(triplet: string | null): number | null {
  const m = /\/\s*([\d.]+)\s*$/.exec(triplet ?? '')
  return m ? Number(m[1]) : null
}

describe.each(cases)('--scrim (%s, %s)', (scheme, mode) => {
  const theme = themeAt(scheme, mode)

  it('is black at a declared alpha', () => {
    const triplet = theme.triplet('scrim')
    expect(triplet, '--scrim').not.toBeNull()
    expect(triplet!.replace(/\s+/g, ' ')).toMatch(/^0 0% 0% \//)
  })

  it('dims body text to at most 60% of its luminance', () => {
    const alpha = scrimAlpha(theme.triplet('scrim'))!
    const fg = theme.rgb('foreground')!
    const page = theme.rgb('background')!
    // Text under the scrim keeps (1 - alpha) of its channel values.
    const before = fg.reduce((a, c) => a + c, 0)
    const after = fg.reduce((a, c) => a + c * (1 - alpha), 0)
    // On a light page the canvas dims too, which is the stronger cue.
    const canvasDrop = page.reduce((a, c) => a + c * alpha, 0)
    expect(after / before <= 0.6 || canvasDrop >= 3 * 0.35 * 255, `alpha ${alpha}`).toBe(true)
  })
})

describe('page-covering overlays use bg-scrim', () => {
  // Not yet moved: the shell's own overlays (U6 owns components/Layout) and
  // the onboarding takeovers, which are a deliberately heavier, blurred
  // full-screen moment rather than a dialog backdrop. The list only shrinks.
  const PENDING = new Set([
    'components/Layout/QuickFileOpen.tsx',
    'components/Layout/CommandPalette.tsx',
    'components/Layout/ChatSearch.tsx',
    'components/Onboarding/OnboardingModal.tsx',
    'components/Onboarding/JourneyPicker.tsx',
    'components/Onboarding/JourneyTour.tsx',
    'components/Onboarding/journeys/workflowsPhone.tsx',
  ])

  /**
   * The class strings in a file that paint a page-covering backdrop: a
   * string literal (quoted or template) holding `inset-0` AND a raw
   * `bg-black/NN`. Comments are removed first, so prose that names the old
   * pattern is never an offender; only a class list can be.
   */
  function rawBackdrops(source: string): number[] {
    const code = source
      .replace(/\/\*[\s\S]*?\*\//g, (c) => c.replace(/[^\n]/g, ' '))
      .replace(/(^|[^:'"`])\/\/[^\n]*/g, (_m, lead: string) => lead)
    const lines: number[] = []
    for (const m of code.matchAll(/(["'`])((?:(?!\1)[^\\]|\\.)*)\1/g)) {
      const body = m[2]!
      if (/(^|\s)inset-0(\s|$)/.test(body) && /(^|\s)bg-black\/\d+(\s|$)/.test(body)) {
        lines.push(code.slice(0, m.index).split('\n').length)
      }
    }
    return lines
  }

  it('finds raw backdrops in class strings, never in comments', () => {
    const src = [
      '// was: className="fixed inset-0 bg-black/50"',
      '/* the old <div className="absolute inset-0 bg-black/40" /> */',
      '{/* inset-0 bg-black/60 */}',
      '<div className="absolute inset-0 bg-scrim" />',
      '<div className="fixed inset-0 z-50 bg-black/50" />',
      'const c = `absolute inset-0 bg-black/40 ${x}`',
    ].join('\n')
    expect(rawBackdrops(src)).toEqual([5, 6])
  })

  function walk(dir: string, out: string[] = []): string[] {
    for (const e of fs.readdirSync(dir, { withFileTypes: true })) {
      const p = path.join(dir, e.name)
      if (e.isDirectory()) {
        if (e.name !== '__tests__' && e.name !== 'gen') walk(p, out)
      } else if (e.name.endsWith('.tsx') && !e.name.includes('.test.')) out.push(p)
    }
    return out
  }

  it('never paints a backdrop with a raw bg-black/NN', () => {
    const offenders: string[] = []
    for (const file of walk(path.join(SRC, 'components'))) {
      const rel = path.relative(SRC, file).split(path.sep).join('/')
      if (PENDING.has(rel)) continue
      for (const line of rawBackdrops(fs.readFileSync(file, 'utf-8'))) offenders.push(`${rel}:${line}`)
    }
    expect(offenders, 'use bg-scrim (index.css --scrim)').toEqual([])
  })

  it('keeps the pending list honest: every entry still has a raw backdrop', () => {
    const stale = [...PENDING].filter((rel) => {
      return rawBackdrops(fs.readFileSync(path.join(SRC, rel), 'utf-8')).length === 0
    })
    expect(stale, 'moved to bg-scrim: drop it from PENDING').toEqual([])
  })
})
