// Copyright (c) 2025 Reliant Labs

import type { CookieJar } from '../sharedSession'

/**
 * One browser's cookie jar, seen from several hosts.
 *
 * jsdom's own jar is bound to one http://localhost document, where a
 * `Domain=reliantlabs.io; Secure` cookie is simply refused. This models the
 * part of RFC 6265 the shared session depends on: a Domain cookie is stored
 * only when the writing host is inside the domain, every host inside it reads
 * it, Max-Age=0 deletes, Secure needs https, and a host outside it sees
 * nothing.
 */
export interface StoredCookie {
  name: string
  value: string
  domain: string
  hostOnly: boolean
  attributes: Record<string, string | true>
}

const inDomain = (host: string, domain: string) => host === domain || host.endsWith(`.${domain}`)

export class FakeCookieBrowser {
  readonly cookies: StoredCookie[] = []
  /** Every Set-Cookie string written, in order, with the host that wrote it. */
  readonly writes: { host: string; cookie: string }[] = []

  jar(host: string, { https = true }: { https?: boolean } = {}): CookieJar {
    return {
      read: () =>
        this.cookies
          .filter((c) => (c.hostOnly ? c.domain === host : inDomain(host, c.domain)))
          .filter((c) => https || !c.attributes.secure)
          .map((c) => `${c.name}=${c.value}`)
          .join('; '),
      write: (cookie) => {
        this.writes.push({ host, cookie })
        const [pair, ...attrs] = cookie.split(';')
        const eq = pair.indexOf('=')
        const name = pair.slice(0, eq).trim()
        const value = pair.slice(eq + 1).trim()
        const attributes: Record<string, string | true> = {}
        for (const attr of attrs) {
          const [k, ...v] = attr.split('=')
          attributes[k.trim().toLowerCase()] = v.length ? v.join('=').trim() : true
        }
        if (attributes.secure && !https) return
        const domainAttr = typeof attributes.domain === 'string' ? attributes.domain.replace(/^\./, '').toLowerCase() : ''
        if (domainAttr && !inDomain(host, domainAttr)) return
        const domain = domainAttr || host
        const hostOnly = !domainAttr
        const index = this.cookies.findIndex((c) => c.name === name && c.domain === domain && c.hostOnly === hostOnly)
        if (index >= 0) this.cookies.splice(index, 1)
        const maxAge = attributes['max-age']
        if (typeof maxAge === 'string' && Number(maxAge) <= 0) return
        this.cookies.push({ name, value, domain, hostOnly, attributes })
      },
    }
  }

  named(prefix: string): StoredCookie[] {
    return this.cookies.filter((c) => c.name.startsWith(prefix))
  }
}

/** A Storage over a Map, for an origin's localStorage. */
export const memoryStorage = (): Storage & { map: Map<string, string> } => {
  const map = new Map<string, string>()
  return {
    map,
    get length() {
      return map.size
    },
    clear: () => map.clear(),
    key: (i: number) => Array.from(map.keys())[i] ?? null,
    getItem: (k: string) => (map.has(k) ? (map.get(k) as string) : null),
    setItem: (k: string, v: string) => void map.set(k, String(v)),
    removeItem: (k: string) => void map.delete(k),
  }
}
