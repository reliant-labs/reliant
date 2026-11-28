import { afterEach, describe, expect, it, vi } from 'vitest'
import { readFileSync } from 'node:fs'
import { join } from 'node:path'
import {
  applyRootFontSize,
  DEFAULT_FONT_SIZE,
  FONT_SIZE_MAP,
  MOBILE_FONT_SIZE_MAP,
  rootFontSizeFor,
} from '../rootFontSize'

describe('rootFontSizeFor', () => {
  it('keeps the desktop scale on desktop routes', () => {
    expect(rootFontSizeFor('md', '/')).toBe('14px')
    expect(rootFontSizeFor('md', '/project/abc')).toBe('14px')
    expect(rootFontSizeFor('xs', '/settings')).toBe('12px')
  })

  it('renders two steps larger on the mobile surface', () => {
    // Tailwind's scale is rem-based, so this is what fixes every screen at
    // once: at a 14px root `text-sm` lands at 13.1px and `text-xs` at 11.4px,
    // against ~17px for iOS body text. A 16px root brings `text-sm` to 15px.
    expect(rootFontSizeFor('md', '/m/chats')).toBe('16px')
    expect(rootFontSizeFor('md', '/m/chats/abc-123')).toBe('16px')
    expect(rootFontSizeFor('md', '/m')).toBe('16px')
  })

  it('preserves the user preference rather than overriding it', () => {
    // Someone who chose the smallest type still gets the smallest type on a
    // phone — the whole scale shifts, it is not replaced by a constant.
    const steps = ['xs', 'sm', 'md', 'lg', 'xl']
    for (const step of steps) {
      const desktop = parseInt(rootFontSizeFor(step, '/'), 10)
      const mobile = parseInt(rootFontSizeFor(step, '/m/chats'), 10)
      expect(mobile).toBeGreaterThan(desktop)
    }
    // And the ordering within each surface is still monotonic.
    const mobileSizes = steps.map((s) =>
      parseInt(rootFontSizeFor(s, '/m/chats'), 10),
    )
    expect([...mobileSizes].sort((a, b) => a - b)).toEqual(mobileSizes)
  })

  it('falls back to the default step for an unknown preference', () => {
    // The value comes from stored settings and can be stale or corrupt; an
    // unreadable root font-size would break every rem-based class in the app.
    expect(rootFontSizeFor('bogus', '/')).toBe(FONT_SIZE_MAP[DEFAULT_FONT_SIZE])
    expect(rootFontSizeFor('bogus', '/m/chats')).toBe(
      MOBILE_FONT_SIZE_MAP[DEFAULT_FONT_SIZE],
    )
  })

  it('defaults to the large step', () => {
    // A new install gets `lg` (15px desktop / 17px mobile), not the former
    // `md`. Pinned because the default is spelled in eight places — seven TS
    // read sites plus the pre-hydration bootstrap in index.html — and a
    // mismatch between them shows up as a flash of the wrong size on load.
    expect(DEFAULT_FONT_SIZE).toBe('lg')
    expect(FONT_SIZE_MAP[DEFAULT_FONT_SIZE]).toBe('15px')
    expect(MOBILE_FONT_SIZE_MAP[DEFAULT_FONT_SIZE]).toBe('17px')
  })

  it('keeps index.html\'s bootstrap default in sync with the constant', () => {
    // The bootstrap runs before any module loads, so it cannot import
    // DEFAULT_FONT_SIZE and has to hardcode the value. Read the real file
    // rather than trusting the comment that says to keep them in sync.
    const html = readFileSync(join(__dirname, '../../../index.html'), 'utf8')

    const step = html.match(/getItem\('appearance\.fontSize'\)\s*\|\|\s*'(\w+)'/)?.[1]
    expect(step).toBe(DEFAULT_FONT_SIZE)

    // …and the resolved-px fallback beside it, for when the map lookup misses.
    const [, mobilePx, desktopPx] =
      html.match(/phoneScale\s*\?\s*'(\d+px)'\s*:\s*'(\d+px)'\)/) ?? []
    expect(desktopPx).toBe(FONT_SIZE_MAP[DEFAULT_FONT_SIZE])
    expect(mobilePx).toBe(MOBILE_FONT_SIZE_MAP[DEFAULT_FONT_SIZE])
  })

  it('does not treat desktop routes starting with m as mobile', () => {
    // `/migrate` would otherwise get phone-sized type on a desktop screen.
    expect(rootFontSizeFor('md', '/migrate')).toBe('14px')
    expect(rootFontSizeFor('md', '/mcp')).toBe('14px')
  })

  it('gives a phone the phone scale on the pages it shares with the desktop', () => {
    // The phone drawer's rows link to responsive desktop pages (Projects, an
    // app's console, Connectors). Sized by path, the same nav drew its rows at
    // 15.9px on /m/chats and 14.1px on /projects: the owner's "the nav's font
    // changes between the root nav and the Agents tab".
    expect(rootFontSizeFor('lg', '/projects', { phoneShaped: true })).toBe('17px')
    expect(rootFontSizeFor('lg', '/forge/p/shop', { phoneShaped: true })).toBe('17px')
    expect(rootFontSizeFor('lg', '/m/chats', { phoneShaped: true })).toBe('17px')
    // A narrow desktop window is not a phone: it keeps desktop sizing.
    expect(rootFontSizeFor('lg', '/projects', { phoneShaped: false })).toBe('15px')
  })
})

/** Stub the window's shape: its width, whether its pointer is coarse, and its URL. */
function shapeWindow({ width, coarse, path }: { width: number; coarse: boolean; path: string }) {
  vi.spyOn(window, 'innerWidth', 'get').mockReturnValue(width)
  vi.stubGlobal('matchMedia', (query: string) => ({
    matches: coarse && query.includes('pointer: coarse'),
    media: query,
    addEventListener: () => {},
    removeEventListener: () => {},
  }))
  window.history.replaceState(null, '', path)
}

function resetWindow() {
  vi.restoreAllMocks()
  vi.unstubAllGlobals()
  localStorage.clear()
  window.history.replaceState(null, '', '/')
  document.documentElement.style.fontSize = ''
}

describe('applyRootFontSize', () => {
  afterEach(resetWindow)

  it('applies the phone scale to a desktop page on a phone', () => {
    shapeWindow({ width: 390, coarse: true, path: '/projects' })
    applyRootFontSize('lg')
    expect(document.documentElement.style.fontSize).toBe('17px')
  })

  it('keeps desktop sizing in a narrow desktop window', () => {
    shapeWindow({ width: 390, coarse: false, path: '/projects' })
    applyRootFontSize('lg')
    expect(document.documentElement.style.fontSize).toBe('15px')
  })

  it('keeps desktop sizing on a phone that asked for the desktop app', () => {
    localStorage.setItem('reliant-force-desktop', '1')
    shapeWindow({ width: 390, coarse: true, path: '/projects' })
    applyRootFontSize('lg')
    expect(document.documentElement.style.fontSize).toBe('15px')
  })
})

/**
 * Runs index.html's pre-hydration bootstrap (the inline script that reads
 * `appearance.fontSize`) against the stubbed window, and returns the root
 * font size it set. The bootstrap is a copy of the decision above that cannot
 * import it, so it is held to the same cases by running it, not by reading it.
 */
function runBootstrap(): string {
  const html = readFileSync(join(__dirname, '../../../index.html'), 'utf8')
  const script = [...html.matchAll(/<script>([\s\S]*?)<\/script>/g)]
    .map((match) => match[1])
    .find((body) => body.includes("appearance.fontSize"))
  if (!script) throw new Error('index.html has no font-size bootstrap')
  document.documentElement.style.fontSize = ''
  new Function(script)()
  return document.documentElement.style.fontSize
}

describe("index.html's bootstrap", () => {
  afterEach(resetWindow)

  it('sizes a desktop page on a phone at the phone scale', () => {
    shapeWindow({ width: 390, coarse: true, path: '/projects' })
    expect(runBootstrap()).toBe('17px')
  })

  it('sizes the same page in a narrow desktop window at the desktop scale', () => {
    shapeWindow({ width: 390, coarse: false, path: '/projects' })
    expect(runBootstrap()).toBe('15px')
  })

  it('honours the desktop opt-out, from the URL or remembered', () => {
    shapeWindow({ width: 390, coarse: true, path: '/projects?desktop=1' })
    expect(runBootstrap()).toBe('15px')
    shapeWindow({ width: 390, coarse: true, path: '/projects' })
    localStorage.setItem('reliant-force-desktop', '1')
    expect(runBootstrap()).toBe('15px')
  })

  it('sizes the phone surface at the phone scale on any device', () => {
    shapeWindow({ width: 1440, coarse: false, path: '/m/chats' })
    expect(runBootstrap()).toBe('17px')
  })
})
