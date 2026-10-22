/**
 * Source-level guards for two bug classes that have each shipped twice on the
 * mobile surface and that normal component tests structurally cannot catch.
 *
 * Both are invisible to typecheck, lint, and rendering tests:
 *
 *   1. `useParams({ from: "<path>" })` — mobile routes are nested under
 *      `_authenticated` → `_mobile`, so a route's registered id is NOT its
 *      path. Passing the path throws "Could not find an active match" at
 *      runtime and strands the user on the desktop error page, which has no
 *      tab bar and no back link. It typechecks, and a test that builds its own
 *      flat route tree resolves the path just fine — which is exactly how it
 *      shipped twice.
 *
 *   2. rem-based sizing on touch targets — the app's root font-size is 14px,
 *      so Tailwind's rem classes render at 87.5%: `h-10` is 35px and `h-11` is
 *      38.5px, both under the 44px minimum. jsdom does not apply the
 *      stylesheet, so no rendering test can measure this.
 *
 * Reading source text is a blunt instrument, but these are exactly the cases
 * where the runtime under test can't tell you the truth.
 */

import { describe, expect, it } from 'vitest'
import { readdirSync, readFileSync } from 'node:fs'
import { dirname, join } from 'node:path'
import { fileURLToPath } from 'node:url'

const MOBILE_DIR = join(dirname(fileURLToPath(import.meta.url)), '..')

function mobileSourceFiles(): { name: string; text: string }[] {
  return readdirSync(MOBILE_DIR)
    .filter((f) => f.endsWith('.tsx') || f.endsWith('.ts'))
    .map((name) => ({
      name,
      text: readFileSync(join(MOBILE_DIR, name), 'utf8'),
    }))
}

describe('mobile source guards', () => {
  it('never passes a route path to useParams({ from })', () => {
    // The fix in every case is `useParams({ strict: false })`, which resolves
    // against whatever route actually matched.
    const offenders = mobileSourceFiles()
      .filter(({ text }) => /useParams\(\s*\{\s*from:/.test(text))
      .map(({ name }) => name)

    expect(
      offenders,
      `useParams({ from: ... }) is unsafe under the _authenticated/_mobile ` +
        `nesting — use useParams({ strict: false }). Offenders: ${offenders.join(', ')}`,
    ).toEqual([])
  })

  it('sizes interactive targets in explicit px, not rem classes', () => {
    // `h-10`/`h-11`/`h-9` on a tappable element is the tell. Non-interactive
    // chrome (a fixed-height header bar) is fine at any size, so this only
    // looks at lines that also carry an interactive affordance.
    const offenders: string[] = []

    for (const { name, text } of mobileSourceFiles()) {
      text.split('\n').forEach((line, i) => {
        const hasRemSize = /\bh-(9|10|11)\b/.test(line)
        const isInteractive = /active:|onClick|role="button"/.test(line)
        if (hasRemSize && isInteractive) {
          offenders.push(`${name}:${i + 1}`)
        }
      })
    }

    expect(
      offenders,
      `Touch targets must use explicit min-h-[44px]/min-w-[44px] — at a 14px ` +
        `root, h-10 is 35px and h-11 is 38.5px. Offenders: ${offenders.join(', ')}`,
    ).toEqual([])
  })

  it('checks a meaningful number of files', () => {
    // Guards that silently match nothing are worse than no guards. If the
    // directory is reorganised, this fails loudly rather than passing vacuously.
    expect(mobileSourceFiles().length).toBeGreaterThan(5)
  })
})
