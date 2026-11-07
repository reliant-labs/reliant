import { describe, expect, it } from 'vitest'
import { isSafeReturnTo } from '@/lib/returnTo'

describe('isSafeReturnTo', () => {
  it.each([
    '/',
    '/settings/billing',
    '/chat/abc?tab=files#msg-3',
    '/a/../b',
    '/claim?code=x%2Fy',
  ])('admits the same-origin path %s', (path) => {
    expect(isSafeReturnTo(path)).toBe(true)
  })

  it.each([
    ['empty', ''],
    ['null', null],
    ['undefined', undefined],
    ['relative without a slash', 'settings'],
    ['absolute URL', 'https://evil.com/x'],
    ['javascript: URL', 'javascript:alert(1)'],
    ['protocol-relative', '//evil.com/x'],
    // The browser reads a backslash as a slash: `/\evil.com` is `//evil.com`.
    ['a backslash', '/\\evil.com'],
    ['a backslash pair', '/\\\\evil.com'],
    // Tabs and newlines are stripped before parsing.
    ['a tab', '/\t/evil.com'],
    ['a newline', '/\n/evil.com'],
    ['a carriage return', '/\r/evil.com'],
    // Dot segments that resolve to a `//` path.
    ['a dot segment to //', '/a/..//evil.com'],
    ['a dot segment to //, backslashed', '/a/..\\/evil.com'],
  ])('refuses %s', (_name, path) => {
    expect(isSafeReturnTo(path)).toBe(false)
  })
})
