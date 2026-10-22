/**
 * The root route mounts several overlays on EVERY route by design, so that
 * users on /settings and /workflow/* still get toasts and modals. Two of them
 * anchor to desktop chrome and must suppress themselves on the mobile surface
 * — these tests pin that, because the failure mode is subtle (a coachmark
 * pointing at nothing, or a tour step telling a phone user to open a page the
 * mobile surface doesn't have).
 */

import { describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'
import { SurfaceProvider } from '../../../lib/surfaceContext'

// The real components pull in stores, routers and query clients; this suite is
// about the surface gate, so everything below the gate is mocked out. Each
// mock renders a marker so "suppressed" vs "rendered" is observable.
vi.mock('../../../store/contextualTipsStore', () => ({
  useContextualTipsStore: (selector: (s: unknown) => unknown) =>
    selector({
      isInitialized: true,
      loadFailed: false,
      activeTipId: 'tip-1',
      loadState: vi.fn(),
      reevaluate: vi.fn(),
      confirmTipShown: vi.fn(),
      clearActiveTip: vi.fn(),
      dismissTip: vi.fn(),
      disableAllTips: vi.fn(),
      subscribeToSources: vi.fn(() => vi.fn()),
    }),
}))

vi.mock('../../../store/tourStore', () => ({
  useTourStore: (selector: (s: unknown) => unknown) =>
    selector({ isInitialized: true, hasCompletedOnboarding: true }),
}))

vi.mock('../../Onboarding/useTourNavigation', () => ({
  useTourNavigation: () => ({ isWizardActive: false }),
}))

vi.mock('../../Onboarding/contextualTipsRegistry', () => ({
  CONTEXTUAL_TIP_DEFINITIONS: [
    { id: 'tip-1', title: 'A tip', body: 'body', anchor: '[data-x]' },
  ],
}))

vi.mock('../../Onboarding/ContextualTipCoachmark', () => ({
  ContextualTipCoachmark: () => <div data-testid="coachmark">coachmark</div>,
}))

vi.mock('@/hooks/useAnonSignInNudge', () => ({
  useAnonSignInNudge: () => ({ open: true, dismiss: vi.fn(), close: vi.fn() }),
}))

vi.mock('@tanstack/react-router', () => ({
  useNavigate: () => vi.fn(),
}))

vi.mock('../../ui/Modal', () => ({
  Modal: ({ children }: { children: React.ReactNode }) => (
    <div data-testid="nudge-modal">{children}</div>
  ),
}))

const { ContextualTipsLayer } = await import(
  '../../Onboarding/ContextualTipsLayer'
)
const { AnonSignInNudge } = await import('../../AnonSignInNudge')

describe('surface-gated root overlays', () => {
  describe('ContextualTipsLayer', () => {
    it('renders a coachmark on desktop', () => {
      render(
        <SurfaceProvider surface="desktop">
          <ContextualTipsLayer />
        </SurfaceProvider>,
      )
      expect(screen.getByTestId('coachmark')).toBeInTheDocument()
    })

    it('renders nothing on mobile', () => {
      // Coachmarks position against desktop chrome via DOM selectors that
      // never match here, so one would float unanchored over the phone UI.
      render(
        <SurfaceProvider surface="mobile">
          <ContextualTipsLayer />
        </SurfaceProvider>,
      )
      expect(screen.queryByTestId('coachmark')).not.toBeInTheDocument()
    })

    it('renders nothing in an embed', () => {
      render(
        <SurfaceProvider surface="embed">
          <ContextualTipsLayer />
        </SurfaceProvider>,
      )
      expect(screen.queryByTestId('coachmark')).not.toBeInTheDocument()
    })
  })

  describe('AnonSignInNudge', () => {
    it('renders on desktop when the nudge is due', () => {
      render(
        <SurfaceProvider surface="desktop">
          <AnonSignInNudge />
        </SurfaceProvider>,
      )
      expect(screen.getByTestId('nudge-modal')).toBeInTheDocument()
    })

    it('renders nothing on mobile', () => {
      // It routes into /upgrade, which is not part of the mobile surface —
      // showing it would be a desktop-sized modal leading to a dead end.
      render(
        <SurfaceProvider surface="mobile">
          <AnonSignInNudge />
        </SurfaceProvider>,
      )
      expect(screen.queryByTestId('nudge-modal')).not.toBeInTheDocument()
    })
  })
})
