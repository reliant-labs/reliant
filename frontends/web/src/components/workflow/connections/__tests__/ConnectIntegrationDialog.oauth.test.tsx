// Copyright (c) 2025 Reliant Labs

import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest'
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

const { startOAuth, completeOAuth, invalidate } = vi.hoisted(() => ({
  startOAuth: vi.fn(),
  completeOAuth: vi.fn(),
  invalidate: vi.fn(),
}))

vi.mock('@/api/connection-grpc', () => ({
  connectionGrpc: { startOAuth, completeOAuth },
  connectionErrorMessage: (err: unknown) => (err instanceof Error ? err.message : String(err)),
}))

vi.mock('@/hooks/connection-queries', () => ({
  useCatalogEntry: () => ({
    isLoading: false,
    isError: false,
    data: {
      connection: {
        methods: [{ kind: 'oauth2', available: true, unavailableReason: '', fieldLabels: {} }],
        params: [],
      },
    },
  }),
  useCreateApiKeyConnection: () => ({ mutateAsync: vi.fn(), isPending: false }),
  invalidateConnectionQueries: invalidate,
}))

import { ConnectIntegrationDialog } from '../ConnectIntegrationDialog'

const connection = {
  id: 'conn_1', integrationId: 'slack', authKind: 'oauth2', name: 'Acme',
  accountLabel: 'Acme', status: 'active', isDefault: true,
}

const renderDialog = (props: { onConnected?: (c: unknown) => void; onClose?: () => void } = {}) => {
  const onConnected = props.onConnected ?? vi.fn()
  const onClose = props.onClose ?? vi.fn()
  const view = render(
    <QueryClientProvider client={new QueryClient()}>
      <ConnectIntegrationDialog
        target={{ ref: 'slack', integrationId: 'slack', displayName: 'Slack' }}
        onClose={onClose}
        onConnected={onConnected}
        redirectAfter="/workflow/x"
      />
    </QueryClientProvider>,
  )
  return { ...view, onConnected, onClose }
}

describe('ConnectIntegrationDialog OAuth', () => {
  let assign: ReturnType<typeof vi.fn>

  beforeEach(() => {
    startOAuth.mockReset().mockResolvedValue('https://slack.com/oauth/v2/authorize?state=s')
    completeOAuth.mockReset().mockResolvedValue({ connection, redirectAfter: '/workflow/x' })
    invalidate.mockReset()
    assign = vi.fn()
    vi.stubGlobal('location', { ...window.location, assign })
  })

  afterEach(() => {
    ;(window as unknown as { electronAPI?: unknown }).electronAPI = undefined
    vi.unstubAllGlobals()
  })

  it('web: sends the page to the provider', async () => {
    renderDialog()
    fireEvent.click(screen.getByRole('button', { name: /Continue to Slack/ }))

    await waitFor(() => expect(assign).toHaveBeenCalledWith('https://slack.com/oauth/v2/authorize?state=s'))
    expect(startOAuth).toHaveBeenCalledWith(expect.objectContaining({ integrationId: 'slack', redirectAfter: '/workflow/x' }))
    expect(completeOAuth).not.toHaveBeenCalled()
  })

  it('desktop: waits for the browser, then hands back the new connection without a reload', async () => {
    let finishBrowser: (v: { code: string; state: string }) => void = () => {}
    const bridge = {
      startConnectionOAuthReceiver: vi.fn(async () => ({ flowId: 'f1', redirectUri: 'http://127.0.0.1:51000/callback' })),
      waitForProviderOAuth: vi.fn(() => new Promise<{ code: string; state: string }>((resolve) => { finishBrowser = resolve })),
      cancelProviderOAuth: vi.fn(async () => undefined),
      openExternal: vi.fn(async () => undefined),
    }
    ;(window as unknown as { electronAPI?: unknown }).electronAPI = bridge
    const { onConnected, onClose } = renderDialog()

    fireEvent.click(screen.getByRole('button', { name: /Continue to Slack/ }))

    expect(await screen.findByText(/Finish signing in to Slack in your browser/)).toBeInTheDocument()
    await waitFor(() => expect(bridge.openExternal).toHaveBeenCalledWith('https://slack.com/oauth/v2/authorize?state=s'))
    expect(startOAuth).toHaveBeenCalledWith(expect.objectContaining({ loopbackRedirect: 'http://127.0.0.1:51000/callback' }))

    finishBrowser({ code: 'the-code', state: 'the-state' })

    await waitFor(() => expect(onConnected).toHaveBeenCalledWith(connection))
    expect(completeOAuth).toHaveBeenCalledWith({ code: 'the-code', state: 'the-state' })
    expect(invalidate).toHaveBeenCalled()
    expect(onClose).toHaveBeenCalled()
    expect(assign).not.toHaveBeenCalled()
  })

  it('desktop: cancelling while the browser is open releases the receiver', async () => {
    const bridge = {
      startConnectionOAuthReceiver: vi.fn(async () => ({ flowId: 'f1', redirectUri: 'http://127.0.0.1:51000/callback' })),
      waitForProviderOAuth: vi.fn(() => new Promise(() => {})),
      cancelProviderOAuth: vi.fn(async () => undefined),
      openExternal: vi.fn(async () => undefined),
    }
    ;(window as unknown as { electronAPI?: unknown }).electronAPI = bridge
    const { onClose } = renderDialog()

    fireEvent.click(screen.getByRole('button', { name: /Continue to Slack/ }))
    await waitFor(() => expect(bridge.waitForProviderOAuth).toHaveBeenCalled())
    fireEvent.click(screen.getByRole('button', { name: 'Cancel' }))

    expect(bridge.cancelProviderOAuth).toHaveBeenCalledWith('f1')
    expect(onClose).not.toHaveBeenCalled()
    expect(await screen.findByRole('button', { name: /Continue to Slack/ })).toBeEnabled()
  })
})
