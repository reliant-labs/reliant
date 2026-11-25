/**
 * The in-page, test-only half of the visual harness, installed by
 * `page.addInitScript` before the app's own scripts run.
 *
 * The data is not here: the production build serves the shop fixture pack in
 * the page (fixture mode, src/fixtures/install.ts), every RPC answered behind
 * the app's real interceptors, and src/fixtures/outside.ts is the fetch
 * boundary (the fake Supabase session, health, telemetry; anything else
 * throws). This script only tells that install what the harness wants, and
 * pins what fixture mode does not:
 *
 *   - `window.__forgeFixtures`: the pack clock (FIXED_NOW) and the surface's
 *     overlays (src/fixtures/shop/overlays.ts), read by install.ts;
 *   - the stored theme, before the bootstrap reads it;
 *   - deterministic Math.random (skeleton widths, jitter, generated ids);
 *   - WebSockets that never connect (the terminal), so no reconnect loop runs
 *     on a timer, and a no-op sendBeacon.
 *
 * This function is serialized into the page: it may not close over anything.
 */
export interface ShimOptions {
  /** The pack clock, epoch ms (page.clock.setFixedTime holds Date at it too). */
  now: number
  /** Overlay names, in order (src/fixtures/shop/overlays.ts). */
  overlays: string[]
  theme: 'light' | 'dark'
}

export function pageShim(opts: ShimOptions): void {
  ;(window as unknown as { __forgeFixtures: unknown }).__forgeFixtures = { now: opts.now, overlays: opts.overlays }

  let seed = 0x2f6b3c1d
  Math.random = () => {
    seed = (Math.imul(seed, 1664525) + 1013904223) >>> 0
    return seed / 4294967296
  }

  try {
    localStorage.setItem('appearance.theme', opts.theme)
  } catch {
    /* storage disabled: the app falls back to its defaults */
  }

  class FakeSocket {
    static CONNECTING = 0
    static OPEN = 1
    static CLOSING = 2
    static CLOSED = 3
    url: string
    readyState = 0
    bufferedAmount = 0
    protocol = ''
    extensions = ''
    binaryType = 'blob'
    onopen = null
    onclose = null
    onerror = null
    onmessage = null
    constructor(url: string | URL) {
      this.url = String(url)
    }
    send() {}
    close() {
      this.readyState = 3
    }
    addEventListener() {}
    removeEventListener() {}
    dispatchEvent() {
      return true
    }
  }
  ;(window as unknown as { WebSocket: unknown }).WebSocket = FakeSocket
  if (navigator.sendBeacon) navigator.sendBeacon = () => true
}
