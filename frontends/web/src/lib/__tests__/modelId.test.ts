import { describe, expect, it } from 'vitest'
import { findPinnedModel, splitModelId } from '../modelId'

describe('splitModelId', () => {
  it('splits a driver-qualified id on its last "@"', () => {
    expect(splitModelId('gpt-5.6-sol@codex')).toEqual({ modelId: 'gpt-5.6-sol', driverId: 'codex' })
  })

  it('leaves a bare model id alone', () => {
    expect(splitModelId('gpt-5.6-sol')).toEqual({ modelId: 'gpt-5.6-sol' })
    expect(splitModelId('trailing@')).toEqual({ modelId: 'trailing@' })
  })
})

describe('findPinnedModel', () => {
  const connected = [
    { id: 'claude-5.5-opus@anthropic', name: 'Claude 5.5 Opus' },
    { id: 'gpt-5.6-sol@openai', name: 'GPT-5.6 Sol' },
  ]

  it('prefers the exact pin', () => {
    const models = [...connected, { id: 'gpt-5.6-sol@codex', name: 'GPT-5.6 Sol (Codex)' }]
    expect(findPinnedModel('gpt-5.6-sol@codex', models)?.name).toBe('GPT-5.6 Sol (Codex)')
  })

  it("names a pin whose provider is gone by the same model on a connected provider", () => {
    // Codex disconnected: the old match compared "gpt-5.6-sol" to
    // "gpt-5.6-sol@codex" and found nothing, so the composer printed the raw id.
    expect(findPinnedModel('gpt-5.6-sol@codex', connected)?.name).toBe('GPT-5.6 Sol')
  })

  it('resolves a bare pin', () => {
    expect(findPinnedModel('claude-5.5-opus', connected)?.name).toBe('Claude 5.5 Opus')
  })

  it('finds nothing when no connected provider serves the model', () => {
    expect(findPinnedModel('gpt-6-luna@codex', connected)).toBeUndefined()
  })
})
