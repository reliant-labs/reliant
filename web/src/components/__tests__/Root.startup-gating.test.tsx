import { render, screen, waitFor } from '@testing-library/react'
import { describe, expect, it, vi } from 'vitest'

const mocks = vi.hoisted(() => ({
  initializePrivacy: vi.fn(),
  settingsInitialize: vi.fn(),
  loadProjects: vi.fn(),
  initSentry: vi.fn(),
  isConfigReady: vi.fn(() => true),
  waitForConfig: vi.fn(),
}))

vi.mock('@/store/privacyStore', () => ({
  usePrivacyStore: (selector: (state: { initialize: typeof mocks.initializePrivacy }) => unknown) =>
    selector({ initialize: mocks.initializePrivacy }),
}))

vi.mock('@/services/settingsSync', () => ({
  settingsSync: {
    initialize: mocks.settingsInitialize,
  },
}))

vi.mock('@/store/projectStore', () => ({
  useProjectStore: {
    getState: () => ({
      loadProjects: mocks.loadProjects,
    }),
  },
}))

vi.mock('@/lib/sentry', () => ({
  initSentry: mocks.initSentry,
}))

vi.mock('@/lib/configReady', () => ({
  isConfigReady: mocks.isConfigReady,
  waitForConfig: mocks.waitForConfig,
}))

vi.mock('@/lib/logger', () => ({
  logger: { info: vi.fn(), warn: vi.fn(), error: vi.fn() },
}))

vi.mock('@/components/Layout/LoadingSpinner', () => ({
  LoadingSpinner: () => <div>loading</div>,
}))

vi.mock('@/components/App', () => ({
  App: () => <div>app ready</div>,
}))

describe('Root startup gating', () => {
  it('renders the app without issuing authenticated startup initialization', async () => {
    const { Root } = await import('@/components/Root')

    render(<Root />)

    await waitFor(() => {
      expect(screen.getByText('app ready')).toBeInTheDocument()
    })

    expect(mocks.initializePrivacy).not.toHaveBeenCalled()
    expect(mocks.settingsInitialize).not.toHaveBeenCalled()
    expect(mocks.loadProjects).not.toHaveBeenCalled()
    expect(mocks.initSentry).not.toHaveBeenCalled()
    expect(mocks.waitForConfig).not.toHaveBeenCalled()
  })
})
