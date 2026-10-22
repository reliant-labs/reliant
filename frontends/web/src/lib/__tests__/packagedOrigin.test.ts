import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'

/**
 * PR #173 moved the packaged renderer from `file://` to `app://bundle` so the
 * root-absolute asset paths in the Vite bundle resolve. It changed only files
 * under `electron/`, so every `window.location.protocol === "file:"` test in
 * `web/src` — each one a stand-in for "is this the packaged desktop app" —
 * silently stopped matching the packaged app.
 *
 * These tests pin the packaged-app behaviour to the ORIGIN the packaged app
 * actually has, so a future scheme change fails here instead of in the field.
 */

type TestWindow = Window & {
  electronAPI?: unknown
  RELIANT_CONFIG?: Record<string, unknown>
}

const setOrigin = (origin: string, protocol: string) => {
  Object.defineProperty(window, 'location', {
    configurable: true,
    value: { ...window.location, origin, protocol, href: `${origin}/` },
  })
}

/** The packaged desktop app as it actually runs since v1.7.0. */
const runAsPackagedElectron = () => {
  ;(window as TestWindow).electronAPI = {}
  setOrigin('app://bundle', 'app:')
}

afterEach(() => {
  delete (window as TestWindow).electronAPI
  delete (window as TestWindow).RELIANT_CONFIG
  vi.unstubAllEnvs()
  vi.resetModules()
})

describe('getAppURL under the packaged app:// origin', () => {
  it('does not hand app://bundle to an identity provider as a redirect target', async () => {
    const { getAppURL } = await import('@/lib/constants')
    runAsPackagedElectron()

    // `app://bundle/auth/callback` is reachable only from inside this
    // renderer. A provider redirect there goes nowhere.
    expect(getAppURL()).toBe('https://app.reliantlabs.io')
  })
})

describe('isConfigReady under the packaged app:// origin', () => {
  beforeEach(() => {
    vi.resetModules()
  })

  it('reports not-ready until RELIANT_CONFIG has actually been injected', async () => {
    const { isConfigReady } = await import('@/lib/configReady')
    runAsPackagedElectron()

    // preload.js injects RELIANT_CONFIG asynchronously via postMessage. Before
    // it lands the packaged app has no backend URL, so it is NOT ready.
    expect(isConfigReady()).toBe(false)
  })

  it('reports ready once RELIANT_CONFIG is present', async () => {
    const { isConfigReady } = await import('@/lib/configReady')
    runAsPackagedElectron()
    ;(window as TestWindow).RELIANT_CONFIG = { grpcUrl: 'http://127.0.0.1:9090' }

    expect(isConfigReady()).toBe(true)
  })
})

describe('waitForConfig under the packaged app:// origin', () => {
  it('waits for the async RELIANT_CONFIG injection instead of returning immediately', async () => {
    vi.resetModules()
    const { waitForConfig } = await import('@/lib/configReady')
    runAsPackagedElectron()

    let settled = false
    const pending = waitForConfig(2000).then(() => {
      settled = true
    })

    // Nothing injected yet: the packaged app must still be waiting.
    await Promise.resolve()
    expect(settled).toBe(false)

    ;(window as TestWindow).RELIANT_CONFIG = { grpcUrl: 'http://127.0.0.1:9090' }
    window.postMessage(
      { type: 'reliant-config-ready', config: { grpcUrl: 'http://127.0.0.1:9090' } },
      '*',
    )

    await pending
    expect(settled).toBe(true)
  })
})

describe('transport selection under the packaged app:// origin', () => {
  it('never treats the packaged renderer as a same-origin (Vite-proxy) surface', async () => {
    const { isSameOriginTransport } = await import('@/lib/protocol')
    runAsPackagedElectron()

    // There is no dev-server proxy inside the packaged app. A same-origin RPC
    // would resolve to app://bundle/reliant.v1.* and hit the SPA's index.html.
    expect(isSameOriginTransport()).toBe(false)
  })
})
