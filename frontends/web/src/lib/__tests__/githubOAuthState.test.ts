// Copyright (c) 2025 Reliant Labs

import { describe, expect, it } from 'vitest'
import { decodeGitHubState, parseLoopbackRedirect } from '@/lib/githubOAuthState'

const b64url = (s: string) =>
  btoa(String.fromCharCode(...new TextEncoder().encode(s)))
    .replace(/\+/g, '-')
    .replace(/\//g, '_')
    .replace(/=+$/, '')

const state = (payload: unknown) => `${b64url(JSON.stringify(payload))}.${b64url('sig')}`

describe('decodeGitHubState', () => {
  it('reads lb and rt from the signed payload', () => {
    expect(decodeGitHubState(state({ uid: 'u', lb: 'http://127.0.0.1:5000/callback', rt: '/x' }))).toEqual({
      lb: 'http://127.0.0.1:5000/callback',
      rt: '/x',
    })
  })

  it('decodes multi-byte payloads', () => {
    expect(decodeGitHubState(state({ rt: '/café' }))?.rt).toBe('/café')
  })

  it.each([undefined, '', 'nodot', '.sig', 'not base64!.sig', `${b64url('[]')}.s`, `${b64url('"s"')}.s`])(
    'returns null for %j',
    (value) => {
      expect(decodeGitHubState(value)).toBeNull()
    },
  )

  it('has no lb for a web-started flow', () => {
    expect(decodeGitHubState(state({ uid: 'u', rt: '/' }))?.lb).toBeUndefined()
  })
})

describe('parseLoopbackRedirect', () => {
  it.each([
    'http://127.0.0.1:5555/callback',
    'http://localhost:1/x?y=1',
    'http://[::1]:8080/callback',
  ])('accepts %s', (value) => {
    expect(parseLoopbackRedirect(value)).not.toBeNull()
  })

  it.each([
    undefined,
    '',
    'https://127.0.0.1:5555/callback',
    'http://127.0.0.1/callback',
    'http://127.0.0.1:80/callback',
    'http://127.0.0.2:5555/callback',
    'http://evil.com:5555/callback',
    'http://127.0.0.1.evil.com:5555/callback',
    'http://user:pw@127.0.0.1:5555/callback',
    'http://127.0.0.1:5555/callback#frag',
    'javascript:alert(1)',
    'app://bundle/callback',
  ])('rejects %j', (value) => {
    expect(parseLoopbackRedirect(value)).toBeNull()
  })
})
