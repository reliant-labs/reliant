/**
 * Resume gating — the mobile surface's ONLY daemon write.
 *
 * `daemonResume: true` with `daemonManage: false` means this table is the
 * entire scope boundary for daemon mutation on a phone. A regression that
 * flips `resumable` on for ACTIVE or PENDING doesn't fail loudly; it produces
 * a button that either no-ops against the control plane or races a start
 * that's already in flight, so it's pinned here per status.
 */

import { describe, expect, it } from 'vitest'
import { DaemonStatus, DaemonSize } from '@/gen/controlplane/v1/public/shared_pb'
import type { Daemon } from '@/services/controlPlane/daemon'
import {
  canResume,
  heartbeatMs,
  presentDaemon,
  sizeLabel,
} from '../daemonPresentation'

function daemon(overrides: Partial<Daemon> = {}): Daemon {
  return { id: 'd1', name: 'box', status: DaemonStatus.ACTIVE, ...overrides } as Daemon
}

describe('canResume', () => {
  it('offers Resume for a suspended daemon', () => {
    expect(canResume(daemon({ status: DaemonStatus.SUSPENDED }))).toBe(true)
  })

  it('offers Resume for a disconnected daemon', () => {
    expect(canResume(daemon({ status: DaemonStatus.DISCONNECTED }))).toBe(true)
  })

  it('does NOT offer Resume for a running daemon', () => {
    // The headline rule: a machine that is already up must not present a
    // write action on a surface that only has one.
    expect(canResume(daemon({ status: DaemonStatus.ACTIVE }))).toBe(false)
  })

  it('does NOT offer Resume for a daemon that is already starting', () => {
    // PENDING is mid-start; Resume here duplicates the start in flight.
    expect(canResume(daemon({ status: DaemonStatus.PENDING }))).toBe(false)
  })

  it('does NOT offer Resume for a failed daemon', () => {
    // Recovery needs the desktop recreate path, which is daemonManage.
    expect(canResume(daemon({ status: DaemonStatus.FAILED }))).toBe(false)
  })

  it('does NOT offer Resume for an unrecognized status', () => {
    // A status the client doesn't know about must fail closed, not open.
    expect(canResume(daemon({ status: 99 as DaemonStatus }))).toBe(false)
  })
})

describe('presentDaemon', () => {
  it('labels each known status', () => {
    expect(presentDaemon(daemon({ status: DaemonStatus.ACTIVE })).label).toBe('Active')
    expect(presentDaemon(daemon({ status: DaemonStatus.SUSPENDED })).label).toBe('Suspended')
    expect(presentDaemon(daemon({ status: DaemonStatus.DISCONNECTED })).label).toBe('Disconnected')
    expect(presentDaemon(daemon({ status: DaemonStatus.FAILED })).label).toBe('Failed')
    expect(presentDaemon(daemon({ status: DaemonStatus.PENDING })).label).toBe('Starting')
  })

  it('falls back to Unknown rather than rendering an empty pill', () => {
    expect(presentDaemon(daemon({ status: 99 as DaemonStatus })).label).toBe('Unknown')
  })

  it('uses only semantic token classes', () => {
    // Guards the styling contract: a raw hex or arbitrary value here would
    // break one of the two themes silently.
    for (const status of [
      DaemonStatus.ACTIVE,
      DaemonStatus.PENDING,
      DaemonStatus.SUSPENDED,
      DaemonStatus.DISCONNECTED,
      DaemonStatus.FAILED,
    ]) {
      expect(presentDaemon(daemon({ status })).pillClassName).not.toMatch(/#[0-9a-f]{3,8}\b/i)
    }
  })
})

describe('sizeLabel', () => {
  it('names each tier', () => {
    expect(sizeLabel(daemon({ size: DaemonSize.SMALL }))).toBe('Small')
    expect(sizeLabel(daemon({ size: DaemonSize.XL }))).toBe('XL')
  })

  it('returns empty string when the tier is unset, so no badge renders', () => {
    expect(sizeLabel(daemon({ size: DaemonSize.UNSPECIFIED }))).toBe('')
  })
})

describe('heartbeatMs', () => {
  it('returns null for a daemon that has never checked in', () => {
    expect(heartbeatMs(daemon())).toBeNull()
  })

  it('converts a protobuf timestamp to epoch millis', () => {
    const seconds = 1_700_000_000
    const value = heartbeatMs(
      daemon({ lastHeartbeat: { seconds: BigInt(seconds), nanos: 0 } as never }),
    )
    expect(value).toBe(seconds * 1000)
  })
})
