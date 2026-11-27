import { readdirSync, readFileSync } from 'node:fs'
import { join } from 'node:path'
import { describe, expect, it } from 'vitest'

/**
 * The phone's screens size text by type role (`text-meta`, `text-ui`,
 * `text-section`, `text-copy`, `text-code`; tailwind.config.js), the scale the
 * phone drawer's rows use. Before this they mixed in Tailwind's raw steps, so
 * an equivalent row read one size in the drawer and another on the screen
 * under it. A raw `text-xs` / `text-sm` / `text-base` here is that drift
 * coming back: pick the role the text IS instead (the web-styling skill,
 * "Type roles").
 */
const MOBILE_DIR = join(__dirname, '..')
const RAW_STEP = /\btext-(?:xs|sm|base)\b/g

describe('the phone screens use the type roles', () => {
  it('has no raw text-xs, text-sm or text-base in components/Mobile', () => {
    const offenders: string[] = []
    for (const name of readdirSync(MOBILE_DIR)) {
      if (!/\.tsx?$/.test(name)) continue
      readFileSync(join(MOBILE_DIR, name), 'utf8')
        .split('\n')
        .forEach((line, index) => {
          for (const match of line.matchAll(RAW_STEP)) offenders.push(`${name}:${index + 1} ${match[0]}`)
        })
    }
    expect(offenders).toEqual([])
  })
})
