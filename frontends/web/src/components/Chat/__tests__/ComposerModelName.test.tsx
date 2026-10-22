import { render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const mocks = vi.hoisted(() => ({ listByProvider: vi.fn() }))

vi.mock('../../../api/client', () => ({
  api: { models: { listByProvider: mocks.listByProvider } },
}))

import { ComposerModelName } from '../ComposerModelName'
import { resetCatalogModelNames } from '../../../hooks/useCatalogModelName'

// A chat still pinned to a disconnected provider's model ("gpt-5.6-sol@codex")
// showed that raw id in the composer's model pill.
describe('ComposerModelName', () => {
  beforeEach(() => {
    vi.clearAllMocks()
    resetCatalogModelNames()
  })

  it('shows the resolved name without asking the catalog', () => {
    render(<ComposerModelName name="Claude 5.5 Opus" pinnedId="claude-5.5-opus@anthropic" />)
    expect(screen.getByText('Claude 5.5 Opus')).toBeInTheDocument()
    expect(mocks.listByProvider).not.toHaveBeenCalled()
  })

  it("names a disconnected provider's pin from that provider's catalog", async () => {
    mocks.listByProvider.mockResolvedValue([
      { id: 'gpt-5.6-sol', name: 'GPT-5.6 Sol' },
      { id: 'gpt-6-luna', name: 'GPT-6 Luna' },
    ])
    const { container } = render(<ComposerModelName pinnedId="gpt-5.6-sol@codex" />)

    expect(await screen.findByText('GPT-5.6 Sol')).toBeInTheDocument()
    expect(mocks.listByProvider).toHaveBeenCalledWith('codex')
    expect(container.textContent).not.toContain('@codex')
  })

  it('falls back to the bare model id, never the raw pin, when the catalog cannot name it', async () => {
    mocks.listByProvider.mockRejectedValue(new Error('offline'))
    const { container } = render(<ComposerModelName pinnedId="gpt-5.6-sol@codex" />)

    await waitFor(() => expect(mocks.listByProvider).toHaveBeenCalled())
    expect(container.textContent).toBe('gpt-5.6-sol')
  })

  it('asks each provider once per session', async () => {
    mocks.listByProvider.mockResolvedValue([{ id: 'gpt-5.6-sol', name: 'GPT-5.6 Sol' }])
    render(<ComposerModelName pinnedId="gpt-5.6-sol@codex" />)
    render(<ComposerModelName pinnedId="gpt-5.6-sol@codex" />)

    await waitFor(() => expect(screen.getAllByText('GPT-5.6 Sol')).toHaveLength(2))
    expect(mocks.listByProvider).toHaveBeenCalledTimes(1)
  })

  it('says auto when nothing is pinned', () => {
    const { container } = render(<ComposerModelName />)
    expect(container.textContent).toBe('auto')
  })
})
