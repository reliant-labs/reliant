/**
 * The screens mounted at /auth, /auth/reset, /auth/verify and the forgot step
 * pick the forge variant with AUTH_PROVIDER "forge", and never touch the
 * Supabase store actions there.
 */
import { render, screen } from '@testing-library/react'
import { beforeEach, describe, expect, it, vi } from 'vitest'

let forge = true
vi.mock('@/lib/auth/forge-api', () => ({ isForgeAuth: () => forge }))
vi.mock('../Auth/forge/ForgeSignIn', () => ({ ForgeSignIn: () => <div>forge sign-in</div> }))
vi.mock('../Auth/forge/ForgeForgotPassword', () => ({ ForgeForgotPassword: () => <div>forge forgot</div> }))
vi.mock('../Auth/forge/ForgeResetPassword', () => ({ ForgeResetPasswordPage: () => <div>forge reset</div> }))
vi.mock('../Auth/forge/ForgeVerifyEmail', () => ({ ForgeVerifyEmailPage: () => <div>forge verify</div> }))

const supabaseStore = vi.fn(() => {
  throw new Error('the Supabase screen rendered in forge mode')
})
vi.mock('@/store/authStore', () => ({ useAuthStore: () => supabaseStore() }))
vi.mock('@tanstack/react-router', () => ({ useNavigate: () => vi.fn(), useSearch: () => ({}) }))

import { AuthScreen } from '../AuthScreen'
import { ForgotPassword } from '../ForgotPassword'
import { ResetPasswordScreen } from '../ResetPasswordScreen'
import { EmailVerification } from '../EmailVerification'

beforeEach(() => {
  forge = true
  supabaseStore.mockClear()
})

describe('auth screens — forge mode', () => {
  it('/auth renders the forge sign-in', () => {
    render(<AuthScreen />)
    expect(screen.getByText('forge sign-in')).toBeTruthy()
    expect(supabaseStore).not.toHaveBeenCalled()
  })

  it('the forgot step, reset and verify render their forge variants', () => {
    render(<ForgotPassword onBackToSignIn={vi.fn()} />)
    render(<ResetPasswordScreen />)
    render(<EmailVerification />)
    expect(screen.getByText('forge forgot')).toBeTruthy()
    expect(screen.getByText('forge reset')).toBeTruthy()
    expect(screen.getByText('forge verify')).toBeTruthy()
    expect(supabaseStore).not.toHaveBeenCalled()
  })

  it('Supabase mode still renders the Supabase screen', () => {
    forge = false
    expect(() => render(<AuthScreen />)).toThrow(/Supabase screen rendered/)
    expect(supabaseStore).toHaveBeenCalled()
  })
})
