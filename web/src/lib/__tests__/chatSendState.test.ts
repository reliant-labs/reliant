import { describe, expect, it } from 'vitest'
import { ChatActivity } from '../../gen/reliant/v1/chat_pb'
import {
  getSendState,
  isChatBusy,
  needsUserAttention,
} from '../chatSendState'

describe('getSendState', () => {
  it('sends normally when the chat is idle', () => {
    const state = getSendState({ activity: ChatActivity.IDLE })
    expect(state.action).toBe('send')
    expect(state.canType).toBe(true)
    // No reason on the happy path — callers render nothing rather than
    // having to special-case an empty string.
    expect(state.reason).toBeNull()
  })

  it('queues rather than rejecting while the agent is running', () => {
    // The backend accepts mid-turn messages and delivers them when the turn
    // yields. Treating RUNNING as "disabled" would be a worse product AND
    // wrong about what the server does.
    const state = getSendState({ activity: ChatActivity.RUNNING })
    expect(state.action).toBe('queue')
    expect(state.canType).toBe(true)
    expect(state.reason).toMatch(/queued/i)
  })

  it('treats awaiting-input as a plain send', () => {
    // The workflow is blocked on the user, so this is the highest-value send
    // in the system — it must not be gated behind a "busy" check.
    const state = getSendState({ activity: ChatActivity.AWAITING_INPUT })
    expect(state.action).toBe('send')
    expect(state.canType).toBe(true)
    expect(state.reason).toBeNull()
  })

  it('resumes on an errored workflow', () => {
    const state = getSendState({ activity: ChatActivity.ERROR })
    expect(state.action).toBe('resume')
    expect(state.canType).toBe(true)
  })

  it('resumes a paused chat', () => {
    const state = getSendState({ activity: ChatActivity.PAUSED })
    expect(state.action).toBe('resume')
    expect(state.reason).toMatch(/paused/i)
  })

  it('prioritizes recovery over a stale RUNNING activity', () => {
    // This is the bug the ordering exists to prevent: the backend sets
    // needsRecovery precisely when the DB still says RUNNING but the durable
    // workflow is gone. Reporting "queue" there promises delivery by a
    // workflow that no longer exists.
    const state = getSendState({
      activity: ChatActivity.RUNNING,
      needsRecovery: true,
    })
    expect(state.action).toBe('resume')
    expect(state.reason).toMatch(/stopped/i)
  })

  it('blocks typing in an archived chat', () => {
    const state = getSendState({
      activity: ChatActivity.IDLE,
      isArchived: true,
    })
    expect(state.action).toBe('blocked')
    expect(state.canType).toBe(false)
    expect(state.reason).toMatch(/archived/i)
  })

  it('reports archived ahead of recovery', () => {
    // Both apply; archive is the one the user must resolve first and the one
    // resuming cannot fix.
    const state = getSendState({
      activity: ChatActivity.RUNNING,
      needsRecovery: true,
      isArchived: true,
    })
    expect(state.action).toBe('blocked')
    expect(state.reason).toMatch(/archived/i)
  })

  it('blocks execution but still allows composing without a daemon', () => {
    // Users should be able to write while a daemon spins up; only delivery
    // is gated. canType stays true so the composer is not dead.
    const state = getSendState({
      activity: ChatActivity.IDLE,
      hasActiveDaemon: false,
    })
    expect(state.action).toBe('blocked')
    expect(state.canType).toBe(true)
    expect(state.reason).toMatch(/daemon/i)
  })

  it('lets recovery take precedence over a missing daemon', () => {
    // Resuming is the action that will itself bring a daemon back, so
    // reporting "no daemon" here would send the user down a dead end.
    const state = getSendState({
      activity: ChatActivity.ERROR,
      hasActiveDaemon: false,
    })
    expect(state.action).toBe('resume')
  })

  it('defaults to sendable for an unknown activity value', () => {
    // Proto enums grow. A new value must not silently disable every
    // composer in the product.
    const state = getSendState({ activity: 99 as ChatActivity })
    expect(state.action).toBe('send')
    expect(state.canType).toBe(true)
  })
})

describe('isChatBusy', () => {
  it('counts running and awaiting-input as busy', () => {
    expect(isChatBusy({ activity: ChatActivity.RUNNING })).toBe(true)
    expect(isChatBusy({ activity: ChatActivity.AWAITING_INPUT })).toBe(true)
  })

  it('does not count idle, paused, or errored as busy', () => {
    expect(isChatBusy({ activity: ChatActivity.IDLE })).toBe(false)
    expect(isChatBusy({ activity: ChatActivity.PAUSED })).toBe(false)
    expect(isChatBusy({ activity: ChatActivity.ERROR })).toBe(false)
  })

  it('is not busy when recovery is needed despite a RUNNING activity', () => {
    // A stuck spinner on a dead workflow is the exact symptom this prevents.
    expect(
      isChatBusy({ activity: ChatActivity.RUNNING, needsRecovery: true }),
    ).toBe(false)
  })

  it('differs from getSendState for awaiting-input', () => {
    // Documents the distinction the two helpers exist to preserve: busy for
    // spinner purposes, but immediately sendable for composer purposes.
    const input = { activity: ChatActivity.AWAITING_INPUT }
    expect(isChatBusy(input)).toBe(true)
    expect(getSendState(input).action).toBe('send')
  })
})

describe('needsUserAttention', () => {
  it('flags awaiting-input, error, and recovery', () => {
    expect(
      needsUserAttention({ activity: ChatActivity.AWAITING_INPUT }),
    ).toBe(true)
    expect(needsUserAttention({ activity: ChatActivity.ERROR })).toBe(true)
    expect(
      needsUserAttention({ activity: ChatActivity.IDLE, needsRecovery: true }),
    ).toBe(true)
  })

  it('does not flag healthy chats', () => {
    expect(needsUserAttention({ activity: ChatActivity.IDLE })).toBe(false)
    expect(needsUserAttention({ activity: ChatActivity.RUNNING })).toBe(false)
  })

  it('never flags an archived chat', () => {
    // Archived chats can't be acted on, so badging one is pure noise —
    // and on mobile this feeds notifications.
    expect(
      needsUserAttention({
        activity: ChatActivity.AWAITING_INPUT,
        isArchived: true,
      }),
    ).toBe(false)
  })
})
