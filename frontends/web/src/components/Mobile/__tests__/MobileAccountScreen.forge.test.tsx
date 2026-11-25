/** /m/account in forge mode: who is signed in comes from the identity, not the synthetic Supabase user. */
import { describe, expect, it, vi } from 'vitest'
import { render, screen } from '@testing-library/react'

// The synthetic user forge-session installs: no identities, no metadata.
const user = { email: 'stale@example.com', identities: [], user_metadata: {}, app_metadata: { provider: 'forge' } }
const identity = { id: 'u1', email: 'ada@example.com', name: 'Ada L', provider: 'forge' }

vi.mock('@tanstack/react-router', () => ({ useNavigate: () => vi.fn() }))
vi.mock('@/lib/auth/forge-api', () => ({ isForgeAuth: () => true }))
vi.mock('../../../store/authStore', () => ({
  useAuthStore: (selector: (s: unknown) => unknown) => selector({ user, identity, signOut: vi.fn() }),
}))
vi.mock('../../../services/settingsSync', () => ({
  settingsSync: { setSetting: vi.fn() },
  SETTINGS_KEYS: { THEME: 'appearance.theme' },
}))
vi.mock('../../../hooks/useOrgs', () => ({
  useActiveOrg: () => ({ canSwitch: false, orgs: [], active: null, activeOrgId: null, select: vi.fn() }),
}))

const { MobileAccountScreen } = await import('../MobileAccountScreen')

describe('MobileAccountScreen (forge mode)', () => {
  it('shows the identity name and email', () => {
    render(<MobileAccountScreen />)
    expect(screen.getByText('Ada L')).toBeInTheDocument()
    expect(screen.getByText('ada@example.com')).toBeInTheDocument()
    expect(screen.queryByText('stale@example.com')).not.toBeInTheDocument()
    expect(screen.queryByText('Anonymous session')).not.toBeInTheDocument()
  })
})
