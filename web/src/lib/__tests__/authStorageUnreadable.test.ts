import { beforeEach, afterEach, describe, expect, it, vi } from 'vitest'

/**
 * The half-authenticated state, reproduced at the seam that produced it.
 *
 * Evidence (packaged build, ~/Library/Logs/reliant-local/main.log): six launches
 * logged "[AuthStorage] Failed to decrypt auth data", and the macOS keychain
 * item "reliant-local Safe Storage" carries cdat 20260826224228Z — 18:42:28
 * local, the exact timestamp of the last such failure. The item is deleted and
 * recreated on every prod-local rebuild, and each recreation mints a NEW random
 * key, so blobs written under the old key can never be decrypted again.
 *
 * The damage was done by how that was REPORTED. `auth:load` answered a bare
 * null, which is byte-for-byte what "no session stored" returns. Supabase's
 * storage adapter therefore saw no session and `getToken()` resolved null,
 * while the in-memory auth store still held a user from a prior
 * `onAuthStateChange` — so the UI read as signed in and every RPC went out
 * with no Authorization header, producing the server's
 * "missing authorization token" (the empty-header branch at
 * internal/grpc/interceptors/auth.go:182 — an expired token takes a different
 * branch and says "invalid or expired token", so expiry is ruled out).
 *
 * These tests drive the REAL storage adapter through the real Supabase client
 * rather than a copy of it, so they fail if the wiring regresses and not just
 * if a helper changes.
 */

// `src/test/setup.ts` replaces this module globally with a stub so that
// importing grpc-client doesn't need real Supabase credentials. This suite is
// specifically about the REAL storage adapter, so it opts back in.
vi.unmock('@/lib/supabase')

const authLoad = vi.fn()
const authSave = vi.fn().mockResolvedValue({ success: true })
const authClear = vi.fn().mockResolvedValue({ success: true })

const storedSession = () => ({
  access_token: 'stored-access-token',
  refresh_token: 'stored-refresh-token',
  // Comfortably in the future: an expired session would send auth-js down its
  // refresh path and make the test about the network instead of storage.
  expires_at: Math.floor(Date.now() / 1000) + 3600,
  expires_in: 3600,
  token_type: 'bearer',
  user: { id: 'e08d19f2-50b1-4e2e-babd-d78ac2f49269' },
})

describe('Electron auth storage: unreadable session vs no session', () => {
  beforeEach(() => {
    vi.resetModules()
    vi.clearAllMocks()
    // createClient() rejects a blank key, and the repo ships no Supabase
    // credentials for tests (the global mock in src/test/setup.ts exists to
    // dodge exactly this). A syntactically valid dummy is enough — these tests
    // never reach the network; every read is served by the stubbed authLoad.
    vi.stubEnv('VITE_SUPABASE_URL', 'http://127.0.0.1:54321')
    vi.stubEnv('VITE_SUPABASE_ANON_KEY', 'test-anon-key')
    // Attach to the REAL jsdom window rather than replacing it: supabase.ts
    // transitively reads window.location (constants → protocol) at import time,
    // and a plain object stub would leave that undefined.
    ;(window as unknown as Record<string, unknown>).electronAPI = {
      authLoad,
      authSave,
      authClear,
    }
  })

  afterEach(async () => {
    // auth-js starts an auto-refresh ticker inside createClient. Left running,
    // it re-enters the storage adapter after the test has torn down its
    // electronAPI stub and logs an unrelated failure into the next file's
    // output. Stopping it keeps this suite's noise inside this suite.
    try {
      const { supabase } = await import('../supabase')
      supabase.auth.stopAutoRefresh()
    } catch {
      // Import failed (e.g. a test that never got that far) — nothing to stop.
    }
    delete (window as unknown as Record<string, unknown>).electronAPI
    vi.unstubAllEnvs()
  })

  it('hands the stored session to Supabase when the blob reads cleanly', async () => {
    authLoad.mockResolvedValue({ success: true, session: storedSession() })

    const { supabase } = await import('../supabase')
    const {
      data: { session },
    } = await supabase.auth.getSession()

    expect(session?.access_token).toBe('stored-access-token')
  })

  it('escalates an unreadable blob instead of silently reporting no session', async () => {
    // The exact shape main.js now returns after a failed decryptString: the
    // blob is gone, the session is null, and the failure is terminal.
    authLoad.mockResolvedValue({
      success: true,
      session: null,
      failure: { reason: 'decrypt_failed', cleared: true, recoverable: false },
    })

    const { supabase, setAuthStorageUnreadableHandler } = await import('../supabase')

    const onUnreadable = vi.fn()
    setAuthStorageUnreadableHandler(onUnreadable)

    const {
      data: { session },
    } = await supabase.auth.getSession()

    // getSession still legitimately reports no session — there IS none to
    // return. The fix is that the failure no longer stops there.
    expect(session).toBeNull()
    expect(onUnreadable).toHaveBeenCalledWith({ reason: 'decrypt_failed' })
  })

  it('stays silent when the user simply is not signed in', async () => {
    authLoad.mockResolvedValue({ success: true, session: null })

    const { supabase, setAuthStorageUnreadableHandler } = await import('../supabase')

    const onUnreadable = vi.fn()
    setAuthStorageUnreadableHandler(onUnreadable)

    await supabase.auth.getSession()

    // This is the distinction the whole fix rests on. A signed-out user must
    // never see a "your saved sign-in could not be read" escalation.
    expect(onUnreadable).not.toHaveBeenCalled()
  })

  it('stays silent when the failure is recoverable', async () => {
    // A locked Linux keyring: the ciphertext is intact and a later read
    // succeeds, so forcing a re-auth here would sign out a healthy user.
    authLoad.mockResolvedValue({
      success: true,
      session: null,
      failure: {
        reason: 'encryption_unavailable',
        cleared: false,
        recoverable: true,
      },
    })

    const { supabase, setAuthStorageUnreadableHandler } = await import('../supabase')

    const onUnreadable = vi.fn()
    setAuthStorageUnreadableHandler(onUnreadable)

    await supabase.auth.getSession()

    expect(onUnreadable).not.toHaveBeenCalled()
  })

  it('escalates once even though every RPC re-reads the session', async () => {
    authLoad.mockResolvedValue({
      success: true,
      session: null,
      failure: { reason: 'decrypt_failed', cleared: true, recoverable: false },
    })

    const { supabase, setAuthStorageUnreadableHandler } = await import('../supabase')

    const onUnreadable = vi.fn()
    setAuthStorageUnreadableHandler(onUnreadable)

    // The captured incident had bursts of concurrent calls (three ListDaemons
    // in seven seconds, then a fan-out of Settings RPCs). One dead blob is one
    // incident, not one per request.
    await supabase.auth.getSession()
    await supabase.auth.getSession()
    await supabase.auth.getSession()

    expect(onUnreadable).toHaveBeenCalledTimes(1)
  })
})
