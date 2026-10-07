/**
 * 12px is the floor for text in the workflow editor, the Workflows area,
 * Automations and Runs (research/WORKFLOW_EDITOR_UX_REVIEW.md §1 issue 10).
 *
 * The review measured the editor's "STARTS WHEN" and section labels at 10.7px
 * — `text-2xs` at the default `lg` root — and that is where the small sizes
 * live: uppercase section labels, node type headers, group captions, type
 * hints. `text-xs` is 0.8125rem, 12.2px at the default root, and is the
 * smallest step these surfaces use. The config panel's own stylesheet is
 * held to the same rem value.
 *
 * Read from the real source files, because the failure mode is someone
 * reaching for `text-2xs` in a new component.
 */
import { describe, expect, it } from 'vitest'
import { readFileSync, readdirSync } from 'node:fs'
import { join } from 'node:path'

const COMPONENTS = join(__dirname, '..', '..')
const AREAS = ['workflow', 'workflows', 'Automations', 'runs'].map((dir) => join(COMPONENTS, dir))

/** `text-xs`'s size (tailwind.config.js), the floor. */
const FLOOR_REM = 0.8125

function sourceFiles(dir: string, acc: string[] = []): string[] {
  for (const entry of readdirSync(dir, { withFileTypes: true })) {
    const full = join(dir, entry.name)
    if (entry.isDirectory()) {
      if (entry.name === '__tests__') continue
      sourceFiles(full, acc)
    } else if (/\.(tsx|ts|css)$/.test(entry.name) && !entry.name.includes('.test.')) {
      acc.push(full)
    }
  }
  return acc
}

describe('workflow surfaces: 12px label floor', () => {
  it('no component uses a type step below text-xs', () => {
    const offenders: string[] = []
    for (const file of AREAS.flatMap((dir) => sourceFiles(dir))) {
      const hits = readFileSync(file, 'utf8').match(/\btext-(2xs|3xs)\b/g)
      if (hits) offenders.push(`${file.slice(COMPONENTS.length + 1)}: ${hits.length}`)
    }
    expect(offenders).toEqual([])
  })

  it('no badge uses its sm size (text-2xs); RunStatusBadge, which defaults to sm, says md', () => {
    const offenders: string[] = []
    for (const file of AREAS.flatMap((dir) => sourceFiles(dir)).filter((f) => f.endsWith('.tsx'))) {
      const source = readFileSync(file, 'utf8')
      for (const tag of source.match(/<(Badge|RunStatusBadge)\b[^>]*>/g) ?? []) {
        const small = /size="sm"/.test(tag) || (tag.startsWith('<RunStatusBadge') && !/size="md"/.test(tag))
        if (small) offenders.push(`${file.slice(COMPONENTS.length + 1)}: ${tag}`)
      }
    }
    expect(offenders).toEqual([])
  })

  it('no stylesheet sets a font size below 0.8125rem', () => {
    const offenders: string[] = []
    for (const file of AREAS.flatMap((dir) => sourceFiles(dir)).filter((f) => f.endsWith('.css'))) {
      for (const match of readFileSync(file, 'utf8').matchAll(/font-size:\s*([\d.]+)rem/g)) {
        if (parseFloat(match[1]!) < FLOOR_REM) offenders.push(`${file.slice(COMPONENTS.length + 1)}: ${match[0]}`)
      }
    }
    expect(offenders).toEqual([])
  })
})
