import { afterEach, describe, expect, it, vi } from 'vitest'

import { getAppURL } from '@/lib/constants'

/**
 * getAppURL answers "what address does the outside world use to reach this
 * app" — the base for OAuth redirects. The interesting case is the packaged
 * desktop app, where the renderer's own origin is a local address that no
 * identity provider can redirect back to.
 */

type TestWindow = Window & { electronAPI?: unknown }

const setOrigin = (origin: string) => {
  Object.defineProperty(window, 'location', {
    configurable: true,
    value: { ...window.location, origin, href: `${origin}/` },
  })
}

const runAsElectron = () => {
  ;(window as TestWindow).electronAPI = {}
}

afterEach(() => {
  delete (window as TestWindow).electronAPI
  vi.unstubAllEnvs()
})

describe('getAppURL', () => {
  it('uses the build-time override when one is configured', () => {
    vi.stubEnv('VITE_APP_URL', 'https://staging.example.test')
    expect(getAppURL()).toBe('https://staging.example.test')
  })

  it('strips a trailing slash from the override so callers can append a path', () => {
    vi.stubEnv('VITE_APP_URL', 'https://staging.example.test/')
    expect(getAppURL()).toBe('https://staging.example.test')
  })

  it('keeps the dev-server origin in a browser, which is a real redirect target', () => {
    setOrigin('http://localhost:3000')
    expect(getAppURL()).toBe('http://localhost:3000')
  })

  it('keeps the deployed origin in a browser', () => {
    setOrigin('https://app.reliantlabs.io')
    expect(getAppURL()).toBe('https://app.reliantlabs.io')
  })

  it.each([
    ['an ephemeral loopback port', 'http://127.0.0.1:61655'],
    ['a named localhost port', 'http://localhost:61851'],
  ])('falls back to the hosted app in Electron served from %s', (_label, origin) => {
    runAsElectron()
    setOrigin(origin)
    expect(getAppURL()).toBe('https://app.reliantlabs.io')
  })

  it('still trusts a remote origin inside Electron', () => {
    runAsElectron()
    setOrigin('https://app.reliantlabs.io')
    expect(getAppURL()).toBe('https://app.reliantlabs.io')
  })
})
