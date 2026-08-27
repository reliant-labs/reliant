/**
 * DaemonRegistryService must talk to reliant's api-server, never admin-server.
 *
 * ── Why this test exists ──────────────────────────────────────────────
 *
 * BOTH hosts answer `/reliant.v1.DaemonRegistryService/*` with HTTP 200, from
 * DIFFERENT DATABASES. Measured against prod with one user's JWT, same minute:
 *
 *   api.reliantapi.com    -> {"daemons":[{... "status":"DAEMON_STATUS_ACTIVE"}]}
 *   admin.reliantapi.com  -> {}
 *
 * control-plane's adapter translates these RPCs into
 * `controlplane.v1.DaemonService/*`, which reads `controlplane.daemons` — a
 * table that only exists in control-plane's own database and never receives a
 * self-hosted daemon's registration. GetDaemon / ResolveDaemon / ResumeDaemon
 * 404 there for the same reason.
 *
 * Routing this client at the control-plane transport therefore produces a
 * confident, successful, WRONG answer: "you have no daemon". That sent users
 * who had a working daemon into onboarding's compute step, and it took four
 * debugging rounds to find because every layer looked healthy in isolation.
 *
 * It only manifests in a PACKAGED build. In dev the renderer is same-origin,
 * getControlPlaneTransport() returns null, and the Vite proxy happens to send
 * `/reliant.v1.*` to reliant-api — the right backend by accident. So no dev
 * test and no manual dev QA can catch a regression here; this test is the
 * guard.
 *
 * DaemonTokenService is deliberately NOT covered: admin-server FORWARDS those
 * three RPCs to reliant rather than reimplementing them, and both hosts were
 * verified to return byte-identical results. Its control-plane preference is
 * correct and must not be "fixed" alongside this.
 */
import { readFileSync } from 'node:fs'
import { resolve } from 'node:path'
import { describe, expect, it } from 'vitest'

// Asserted against the SOURCE rather than by spying on the transport factory.
// A spy cannot discriminate here: getControlPlaneTransport is called directly
// inside the module, not through the module object, so a mocked export is
// never consulted and the test passes either way — verified by reverting the
// fix and watching a spy-based version stay green.
// Resolved from the vitest root (web/) rather than import.meta.url, which is
// not a file: URL under vitest's transform pipeline.
const source = readFileSync(resolve(process.cwd(), 'src/api/grpc-client.ts'), 'utf8')

const daemonRegistryFactory = (): string => {
  const start = source.indexOf('export const createDaemonRegistryClient')
  const end = source.indexOf('export const createDaemonTokenClient')
  expect(start).toBeGreaterThan(-1)
  expect(end).toBeGreaterThan(start)
  return source.slice(start, end)
}

describe('DaemonRegistryService transport', () => {
  it('never resolves through getControlPlaneTransport', () => {
    // The exact regression: `getControlPlaneTransport() ?? getTransport()`
    // returns admin-server in a packaged build, which answers this RPC from a
    // different database and reports zero daemons.
    expect(daemonRegistryFactory()).not.toContain('getControlPlaneTransport')
  })

  it('builds on the reliant api-server transport', () => {
    expect(daemonRegistryFactory()).toContain('getTransport()')
  })

  it('leaves DaemonTokenService on control-plane, which forwards correctly', () => {
    // admin-server FORWARDS these three RPCs to reliant rather than
    // reimplementing them; both hosts were verified to return byte-identical
    // results. Changing them alongside the registry fix would be a regression.
    const start = source.indexOf('export const createDaemonTokenClient')
    const tokenFactory = source.slice(start, start + 400)
    expect(tokenFactory).toContain('getControlPlaneTransport')
  })
})
