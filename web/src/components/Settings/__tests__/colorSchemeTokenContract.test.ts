import { describe, expect, it } from 'vitest'
import fs from 'node:fs'
import path from 'node:path'
import { fileURLToPath } from 'node:url'

const __filename = fileURLToPath(import.meta.url)
const __dirname = path.dirname(__filename)

const THEMES_CSS_PATH = path.resolve(__dirname, '../../../themes/professional-themes.css')
const THEMES_CSS = fs.readFileSync(THEMES_CSS_PATH, 'utf-8')

const COLOR_SCHEMES = [
  'professional-blue',
  'refined-neutral',
  'modern-teal',
  'slate',
  'forest',
  'vibrant-pink',
  'energetic-orange',
  'bold-red',
  'purple-classic',
  'pure-black',
] as const

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
})
