// Copyright (c) 2025 Reliant Labs

/**
 * A method this deployment hasn't set up is described in words an end user
 * can act on. The env-var names that explain it are for whoever runs the
 * deployment: the server log, and this dialog on a dev deployment only.
 */

import { beforeEach, describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { QueryClient, QueryClientProvider } from '@tanstack/react-query'

const { state } = vi.hoisted(() => ({
  state: { isDev: false, methods: [] as Array<{ kind: string; available: boolean; unavailableReason: string; fieldLabels: Record<string, string> }> },
}))

vi.mock('@/lib/constants', async () => ({
  ...(await vi.importActual<typeof import('@/lib/constants')>('@/lib/constants')),
  getIsDev: () => state.isDev,
}))

vi.mock('@/hooks/connection-queries', () => ({
  useCatalogEntry: () => ({
    isLoading: false,
    isError: false,
    data: { connection: { methods: state.methods, params: [] } },
  }),
  useCreateApiKeyConnection: () => ({ mutateAsync: vi.fn(), isPending: false }),
  invalidateConnectionQueries: vi.fn(),
}))

import { ConnectIntegrationDialog } from '../ConnectIntegrationDialog'

const ENV_REASON = 'RELIANT_OAUTH_GMAIL_CLIENT_ID and RELIANT_OAUTH_GMAIL_CLIENT_SECRET are not set'
const oauthUnavailable = { kind: 'oauth2', available: false, unavailableReason: ENV_REASON, fieldLabels: {} }

function renderDialog() {
  return render(
    <QueryClientProvider client={new QueryClient()}>
      <ConnectIntegrationDialog target={{ ref: 'gmail/message.send@1', integrationId: 'gmail', displayName: 'Gmail' }} onClose={vi.fn()} />
    </QueryClientProvider>,
  )
}

describe('ConnectIntegrationDialog when a method is not set up here', () => {
  beforeEach(() => {
    state.isDev = false
  })

  it('every method unavailable: says it cannot be connected here, offers no Connect, names no env var', () => {
    state.methods = [oauthUnavailable]
    renderDialog()

    expect(screen.getByRole('alert')).toHaveTextContent("Gmail can't be connected on this deployment yet.")
    expect(screen.queryByRole('button', { name: /Continue to Gmail/ })).toBeNull()
    expect(screen.queryByRole('button', { name: /Save connection/ })).toBeNull()
    expect(screen.getByRole('button', { name: 'Close' })).toBeInTheDocument()
    expect(document.body).not.toHaveTextContent('RELIANT_OAUTH')
  })

  it('one method unavailable: plain copy for it, and the method that works stays usable', () => {
    state.methods = [oauthUnavailable, { kind: 'api_key', available: true, unavailableReason: '', fieldLabels: {} }]
    renderDialog()

    expect(screen.getByText("Gmail sign-in isn't available on this deployment yet.")).toBeInTheDocument()
    expect(screen.getByRole('button', { name: 'Save connection' })).toBeInTheDocument()
    expect(document.body).not.toHaveTextContent('RELIANT_OAUTH')
  })

  it('a dev deployment also shows the operator the missing settings', () => {
    state.isDev = true
    state.methods = [oauthUnavailable]
    renderDialog()

    expect(screen.getByText('Setup details (dev deployment)')).toBeInTheDocument()
    expect(document.body).toHaveTextContent(ENV_REASON)
  })
})
