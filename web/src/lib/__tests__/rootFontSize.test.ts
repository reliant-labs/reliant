import { describe, expect, it } from 'vitest'
import {
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

  it('falls back to the medium step for an unknown preference', () => {
    // The value comes from stored settings and can be stale or corrupt; an
    // unreadable root font-size would break every rem-based class in the app.
    expect(rootFontSizeFor('bogus', '/')).toBe(FONT_SIZE_MAP.md)
    expect(rootFontSizeFor('bogus', '/m/chats')).toBe(MOBILE_FONT_SIZE_MAP.md)
  })

  it('does not treat desktop routes starting with m as mobile', () => {
    // `/migrate` would otherwise get phone-sized type on a desktop screen.
    expect(rootFontSizeFor('md', '/migrate')).toBe('14px')
    expect(rootFontSizeFor('md', '/mcp')).toBe('14px')
  })
})
