import { describe, expect, it } from 'vitest'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

/**
 * jsdom does not implement `:-webkit-autofill` — the pseudo-class only ever
 * matches after a real Chrome/Safari credential fill, so there is no way to
 * render this state in a component test. These assertions pin the stylesheet
 * contract instead, which is where the white-on-white bug actually lived.
 */

const __dirname = path.dirname(fileURLToPath(import.meta.url))
const INDEX_CSS = fs.readFileSync(path.resolve(__dirname, '../../index.css'), 'utf-8')

function autofillBlock(): string {
  const match = INDEX_CSS.indexOf('-webkit-autofill')
  expect(match, 'no :-webkit-autofill rule in index.css').toBeGreaterThanOrEqual(0)

  const blockStart = INDEX_CSS.indexOf('{', match)
  const blockEnd = INDEX_CSS.indexOf('}', blockStart)
  expect(blockEnd).toBeGreaterThan(blockStart)

  return INDEX_CSS.slice(blockStart + 1, blockEnd)
}

describe('autofill surface contract', () => {
  it('paints an opaque themed fill rather than letting the browser background show', () => {
    const block = autofillBlock()

    // The regression: a `transparent` inset fill leaves Chrome's own near-white
    // autofill background painted, while -webkit-text-fill-color forces the
    // themed foreground on top of it. In dark mode that is white on white.
    expect(block).not.toMatch(/box-shadow:[^;]*transparent/)
    expect(block).toMatch(/box-shadow:[^;]*hsl\(var\(--autofill-bg/)
  })

  it('keeps autofilled text and caret on the themed foreground', () => {
    const block = autofillBlock()

    expect(block).toContain('-webkit-text-fill-color: hsl(var(--foreground))')
    expect(block).toContain('caret-color: hsl(var(--foreground))')
  })

  it('covers every autofillable control, not just bare inputs', () => {
    // A <select> or <textarea> autofill hits the same cascade.
    expect(INDEX_CSS).toContain('select:-webkit-autofill')
    expect(INDEX_CSS).toContain('textarea:-webkit-autofill')
  })

  it('declares color-scheme so native UI follows the app palette', () => {
    // Without this the browser assumes a light page and picks its light
    // autofill palette even when the app is rendering dark.
    expect(INDEX_CSS).toMatch(/:root\s*\{[^}]*color-scheme:\s*light/s)
    expect(INDEX_CSS).toMatch(/\.dark\s*\{[^}]*color-scheme:\s*dark/s)
  })
})
