/**
 * One focus treatment app-wide, and the brand faces as the default type.
 *
 * Focus: the global outline is a solid --ring, and every component ring that
 * replaces it paints `ring-ring` at full strength. The app had grown
 * `ring-ring/40`, `ring-primary/50`, `ring-accent` (reliant's MUTED tint
 * outside `.forge-ui`, so a grey ring) and a `ring-sky-400/60`: five focus
 * colors, some of them faint. forge-ui/ is installed verbatim from forge and
 * scopes `accent` to the action color itself, so it is exempt.
 *
 * Type: Geist and Geist Mono lead --font-sans / --font-mono, and their faces
 * are bundled. The "Geist" Appearance option used to name a font no file
 * provided, so it silently rendered the platform face.
 */
import { describe, expect, it } from 'vitest'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const here = path.dirname(fileURLToPath(import.meta.url))
const SRC = path.resolve(here, '../../..')
const read = (rel: string) => fs.readFileSync(path.join(SRC, rel), 'utf-8')

function sources(dir: string): string[] {
  const out: string[] = []
  for (const entry of fs.readdirSync(dir, { withFileTypes: true })) {
    const full = path.join(dir, entry.name)
    if (entry.isDirectory()) {
      if (!['gen', 'node_modules', 'forge-ui'].includes(entry.name)) out.push(...sources(full))
    } else if (/\.(tsx?|css)$/.test(entry.name) && !/\.test\./.test(entry.name)) {
      out.push(full)
    }
  }
  return out
}

describe('focus ring', () => {
  it('draws the global outline in solid --ring', () => {
    expect(read('index.css')).toMatch(/\*:focus-visible \{\s*outline: 2px solid hsl\(var\(--ring\)\);/)
  })

  it('never paints a focus ring in an alpha of --ring, or in --primary / --accent', () => {
    const stray = /(?<![\w-])(?:[\w-]+:)*focus(?:-visible|-within)?:(?:[\w-]+:)*(?:ring|outline)-(?:ring\/\d+|primary|accent|sky-|blue-)/
    const offenders: string[] = []
    for (const file of sources(SRC)) {
      fs.readFileSync(file, 'utf-8').split('\n').forEach((line, i) => {
        if (stray.test(line)) offenders.push(`${path.relative(SRC, file)}:${i + 1}`)
      })
    }
    expect(offenders, 'use focus-visible:ring-ring (or the global outline)').toEqual([])
  })
})

describe('brand type', () => {
  it('leads the UI and mono stacks with the bundled Geist faces', () => {
    const css = read('index.css')
    expect(css).toMatch(/--font-sans: 'Geist Variable', 'Geist Fallback',/)
    expect(css).toMatch(/--font-mono: 'Geist Mono Variable',/)
    expect(css).toMatch(/:root\[data-font="default"\] \{\s*--font-sans: 'Geist Variable'/)
    // The faces themselves: @fontsource's @font-face rules, from every entry
    // point that loads index.css.
    for (const entry of ['main.tsx', 'findBar/main.tsx']) {
      const body = read(entry)
      expect(body, entry).toContain('import "@fontsource-variable/geist";')
      expect(body, entry).toContain('import "@fontsource-variable/geist-mono";')
      expect(body.indexOf('@fontsource-variable/geist"'), `${entry}: fonts before index.css`).toBeLessThan(body.indexOf('index.css"'))
    }
  })

  it('names no font family that nothing bundles', () => {
    // A bare 'Geist' is what the broken option named: @fontsource registers
    // the faces as 'Geist Variable' / 'Geist Mono Variable'.
    expect(read('index.css')).not.toMatch(/'Geist',/)
  })
})
