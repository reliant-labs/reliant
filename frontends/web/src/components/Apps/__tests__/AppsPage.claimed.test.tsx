/**
 * What /apps says right after a claim: the app is highlighted, and a claimer
 * who already had a free static site is told what happens to this one and what
 * to do about it (a membership claim, W11).
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
vi.mock('../../Layout/AreaShell', () => ({ AreaShell: ({ children }: { children: React.ReactNode }) => <div>{children}</div> }))
vi.mock('../appParts', () => ({ AppEnvRow: () => null }))
vi.mock('@/services/forge/apps', () => ({ isLive: () => false, primaryLink: () => null }))
vi.mock('../../Forge/Console/paths', () => ({ consolePath: () => '/apps/storefront' }))

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
    expect(screen.getByTestId('app-card-storefront')).toHaveAttribute('data-highlighted', 'true')
    expect(scrollIntoView).toHaveBeenCalled()
    expect(screen.queryByTestId('apps-claimed-over-allowance')).toBeNull()
    await waitFor(() => expect(navigate).toHaveBeenCalledWith({ to: '/apps', search: {}, replace: true }))
  })

  it('explains the pause and offers the remedy when the claimer was already at the free-site limit', async () => {
    search = { claimed: 'storefront', overAllowance: true }
    render(<AppsPage />)
    const note = await screen.findByTestId('apps-claimed-over-allowance')
    expect(note).toHaveTextContent(/already have a free static site/i)
    expect(note).toHaveTextContent(/oldest live/i)
    fireEvent.click(screen.getByRole('button', { name: 'subscribe' }))
    expect(goToBilling).toHaveBeenCalledWith('upgrade')
  })
})
