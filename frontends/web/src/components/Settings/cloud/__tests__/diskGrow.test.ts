import { describe, expect, it } from 'vitest'
import { Code, ConnectError } from '@connectrpc/connect'

import { DaemonDiskState } from '@/gen/controlplane/controlplane/v1/shared_pb'
import {
  DAEMON_STORAGE_DENIED_REASON,
  diskGrowPrice,
  diskGrowRange,
  diskGrowStatus,
  isStorageDenied,
  type DiskLike,
} from '../diskGrow'

describe('diskGrowRange', () => {
  it('runs from the next whole step to the plan limit', () => {
    expect(diskGrowRange(160, 320)).toEqual({ min: 170, max: 320 })
  })

  it('starts at the next multiple of 10 for a disk between steps', () => {
    // GrowDaemonDisk refuses a target that is not a multiple of 10.
    expect(diskGrowRange(25, 80)).toEqual({ min: 30, max: 80 })
  })

  it('rounds a limit between steps down', () => {
    expect(diskGrowRange(160, 325)).toEqual({ min: 170, max: 320 })
  })

  it('has no range when the plan offers no growth, or the disk is at or past the limit', () => {
    expect(diskGrowRange(160, 0)).toBeNull()
    expect(diskGrowRange(320, 320)).toBeNull()
    // Grown under a bigger plan, then moved to a smaller one.
    expect(diskGrowRange(400, 320)).toBeNull()
  })

  it('offers the one step left when only one fits', () => {
    expect(diskGrowRange(310, 320)).toEqual({ min: 320, max: 320 })
  })

  it('has no range for a disk of unknown size', () => {
    expect(diskGrowRange(0, 320)).toBeNull()
  })
})

describe('diskGrowPrice', () => {
  const pricing = { storageCentsPerGibMonth: '25.0000' }

  it('prices a grow at the most it adds: the GB added, at the storage price', () => {
    // Storage is pooled across the org, so this is a ceiling, not a promise.
    expect(diskGrowPrice(pricing, { currentGib: 160, targetGib: 200 })).toEqual({ added: '+$10.00/mo' })
    expect(diskGrowPrice(pricing, { currentGib: 120, targetGib: 130 })).toEqual({ added: '+$2.50/mo' })
  })

  it('shows no price without a price list, rather than a guessed one', () => {
    expect(diskGrowPrice(undefined, { currentGib: 160, targetGib: 170 })).toBeNull()
    expect(
      diskGrowPrice({ storageCentsPerGibMonth: 'n/a' }, { currentGib: 160, targetGib: 170 }),
    ).toBeNull()
  })
})

describe('diskGrowStatus', () => {
  const disk = (over: Partial<DiskLike> = {}): DiskLike => ({
    requestedGib: 200,
    capacityGib: 200,
    usableGib: 200,
    state: DaemonDiskState.STABLE,
    message: '',
    ...over,
  })

  it('says nothing about a disk that is the size it was asked to be', () => {
    expect(diskGrowStatus(disk(), true)).toBeNull()
    expect(diskGrowStatus(undefined, true)).toBeNull()
  })

  it('reports a grow in progress, toward the requested size', () => {
    expect(diskGrowStatus(disk({ capacityGib: 160, state: DaemonDiskState.EXPANDING }), true)).toEqual({
      kind: 'expanding',
      targetGib: 200,
    })
  })

  it('reports a failed grow with the server message', () => {
    expect(
      diskGrowStatus(disk({ capacityGib: 160, state: DaemonDiskState.FAILED, message: 'quota exceeded' }), false),
    ).toEqual({ kind: 'failed', targetGib: 200, message: 'quota exceeded' })
  })

  it('asks for a restart when the running guest sees less than the disk holds', () => {
    expect(diskGrowStatus(disk({ usableGib: 160 }), true)).toEqual({ kind: 'restart-to-use', extraGib: 40 })
  })

  it('never asks a stopped machine to restart: it uses the whole disk when it starts', () => {
    expect(diskGrowStatus(disk({ usableGib: 160 }), false)).toBeNull()
  })

  it('reads an unknown usable size as nothing to say, not a disk the guest cannot use', () => {
    expect(diskGrowStatus(disk({ usableGib: 0 }), true)).toBeNull()
  })
})

describe('isStorageDenied', () => {
  it('recognises the plan-limit refusal by its reason header', () => {
    const denied = new ConnectError('target exceeds plan', Code.PermissionDenied, {
      'x-reliant-reason': DAEMON_STORAGE_DENIED_REASON,
    })
    expect(isStorageDenied(denied)).toBe(true)
  })

  it('does not mistake another refusal, or a plain error, for it', () => {
    const sizeDenied = new ConnectError('size', Code.PermissionDenied, { 'x-reliant-reason': 'daemon_size_denied' })
    expect(isStorageDenied(sizeDenied)).toBe(false)
    expect(isStorageDenied(new Error('daemon_storage_denied'))).toBe(false)
  })
})
