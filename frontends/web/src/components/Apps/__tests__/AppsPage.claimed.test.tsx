/**
 * What /apps says right after a claim: the app is highlighted, and a claimer
 * who chose between their two free static sites is told which one is paused and
 * that subscribing undoes it (a membership claim, W11).
 */
import { fireEvent, render, screen, waitFor } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

const navigate = vi.fn()
let search: Record<string, unknown> = {}
vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => navigate,
  useSearch: () => search,
}))

vi.mock('@/hooks/apps-queries', () => ({
  useApps: () => ({
    apps: [{ name: 'storefront', envs: [], previews: 0 }],
    loading: false,
    availability: 'available',
    detail: '',
  }),
}))

const goToBilling = vi.fn()
vi.mock('@/hooks/useGoToBilling', () => ({ useGoToBilling: (from: string) => () => goToBilling(from) }))

vi.mock('../startBuilding', () => ({ useStartBuildingApp: () => vi.fn() }))
vi.mock('@/hooks/observability-queries', () => ({
  useObservabilityOrg: () => ({ orgId: null, isLoading: false, error: null }),
  useAlerts: () => ({ data: [] }),
}))
vi.mock('../../Layout/AreaShell', () => ({ AreaShell: ({ children }: { children: React.ReactNode }) => <div>{children}</div> }))

import { AppsPage } from '../AppsPage'

// jsdom has no layout, so no scrollIntoView; the card scrolls itself into view when highlighted.
const scrollIntoView = vi.fn()
beforeEach(() => {
  vi.clearAllMocks()
  search = {}
  Element.prototype.scrollIntoView = scrollIntoView
})

describe('/apps after a claim', () => {
  it('says the app is yours now, with nothing about an allowance when there is no problem', async () => {
    search = { claimed: 'storefront' }
    render(<AppsPage />)
    expect(await screen.findByTestId('apps-claimed')).toHaveTextContent('storefront is yours now')
    expect(screen.getByTestId('app-row-storefront')).toHaveAttribute('data-highlighted', 'true')
    expect(screen.queryByTestId('apps-claimed-paused')).toBeNull()
    await waitFor(() => expect(navigate).toHaveBeenCalledWith({ to: '/apps', search: {}, replace: true }))
  })

  it.each([
    ['claimed', /you chose free hosting for the site you claimed/i],
    ['existing', /you kept free hosting where it was/i],
  ] as const)('names the reported paused site and offers billing (kept %s)', async (kept, said) => {
    search = { claimed: 'storefront', pausedSite: 'blog', kept }
    render(<AppsPage />)
    const note = await screen.findByTestId('apps-claimed-paused')
    expect(note).toHaveTextContent(said)
    expect(note).toHaveTextContent(/blog/)
    expect(note).toHaveTextContent(/is paused/i)
    expect(note).not.toHaveTextContent(/one static site|10 GiB/i)
    fireEvent.click(screen.getByRole('button', { name: 'Review billing' }))
    expect(goToBilling).toHaveBeenCalledWith('upgrade')
  })
})
