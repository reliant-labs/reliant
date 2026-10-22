/**
 * The font-size preference only works if the whole UI is expressed in units
 * that resolve against the root size. A single `font-size: 11px` — in a
 * stylesheet, or as a `text-[11px]` arbitrary class — opts that element out
 * permanently: it renders identically at every one of the five steps, so a
 * user who enlarges the type to read it gets nothing.
 *
 * That is exactly what had happened. The workflow params panel and the tool
 * renderers were written in hardcoded px, so `.cpv2-section-label` measured
 * 10px at the `xs`, `md` and `xl` preferences alike, and `body`'s own
 * `font-size: 14px` pinned every element that merely INHERITS its size.
 *
 * These tests read the real source files rather than a fixture, because the
 * failure mode is someone reintroducing a px value in a component — which a
 * fixture would never see.
 */
import { describe, expect, it } from 'vitest'
import { readFileSync, readdirSync } from 'node:fs'
import { join } from 'node:path'

const SRC = join(__dirname, '..', '..')

/** Every .tsx/.css file under src, minus tests. */
function sourceFiles(dir: string, acc: string[] = []): string[] {
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name)
    if (entry.isDirectory()) {
      if (entry.name === '__tests__' || entry.name === 'node_modules') continue
      sourceFiles(full, acc)
    } else if (/\.(tsx|css)$/.test(entry.name) && !entry.name.includes('.test.')) {
      acc.push(full)
    }
  }
  return acc
}

describe('font-size preference contract', () => {
  it('body inherits the root size instead of pinning it', () => {
    const css = readFileSync(join(SRC, 'index.css'), 'utf8')
    const body = css.match(/\bbody\s*\{[^}]*\}/)?.[0] ?? ''

    expect(body).toMatch(/font-size:\s*[\d.]+rem/)
    // A px value here silently overrides the preference for everything that
    // inherits, which is most of the app.
    expect(body).not.toMatch(/font-size:\s*\d+px/)
  })

  it('the workflow params panel scales with the preference', () => {
    const panel = readFileSync(
      join(SRC, 'components/workflow/config/config-panel.css'),
      'utf8',
    )
    const pxSizes = panel.match(/font-size:\s*\d+px/g) ?? []

    expect(pxSizes).toEqual([])
    // And it should still be setting sizes at all — a passing test because
    // every declaration was deleted would be worthless.
    expect((panel.match(/font-size:\s*[\d.]+rem/g) ?? []).length).toBeGreaterThan(20)
  })

  it('no component reintroduces an arbitrary px font size', () => {
    // The two permitted exceptions are labels centred inside fixed-size SVG
    // rings, which overflow the circle if they scale. They are annotated with
    // an eslint-disable naming that reason.
    const ALLOWED = new Set([
      'ContextUsageIndicator.tsx',
      'ThreadTabs.tsx',
    ])

    const offenders: string[] = []
    for (const file of sourceFiles(SRC)) {
      const name = file.split('/').pop()!
      if (ALLOWED.has(name)) continue
      const hits = readFileSync(file, 'utf8').match(/text-\[\d+px\]/g)
      if (hits) offenders.push(`${name}: ${[...new Set(hits)].join(', ')}`)
    }

    expect(offenders).toEqual([])
  })

  it('keeps the small steps on the ~1.6 line-height the app inherited', () => {
    // The ~300 call sites these steps replaced were arbitrary classes
    // (`text-[10px]`), which carry no line-height and so inherited body's 1.6.
    // Tailwind's own steps ship a much tighter ratio, so adopting it shortened
    // every one of those line boxes by ~1.8px — visible as the app's spacing
    // collapsing after a reload. Pin the ratio for the steps that took over
    // from arbitrary values.
    const config = readFileSync(join(SRC, '..', 'tailwind.config.js'), 'utf8')

    for (const step of ['3xs', '2xs', 'xs', 'sm']) {
      const key = /^\d/.test(step) ? `"${step}"` : step
      const row = config.match(
        new RegExp(`${key}:\\s*\\["([\\d.]+)rem",\\s*\\{\\s*lineHeight:\\s*"([\\d.]+)rem"`),
      )
      expect(row, `no fontSize entry for ${step}`).toBeTruthy()

      const ratio = Number(row![2]) / Number(row![1])
      expect(ratio, `${step} line-height ratio ${ratio.toFixed(2)}`).toBeGreaterThan(1.55)
      expect(ratio, `${step} line-height ratio ${ratio.toFixed(2)}`).toBeLessThan(1.65)
    }
  })

  it('exposes scale steps below xs so dense UI never needs a px value', () => {
    // The sweep replaced ~300 call sites; text-[9px] and text-[10px] were the
    // two most common, so the scale has to reach that low or the next dense
    // panel reintroduces them out of necessity.
    const config = readFileSync(join(SRC, '..', 'tailwind.config.js'), 'utf8')
    const scale = config.match(/fontSize:\s*\{[\s\S]*?\n\s{6}\}/)?.[0] ?? ''

    expect(scale).toMatch(/"3xs":/)
    expect(scale).toMatch(/"2xs":/)
    // rem throughout — a px step would break the preference for every call
    // site that used it.
    expect(scale).not.toMatch(/:\s*\["?\d+px/)
  })
})
