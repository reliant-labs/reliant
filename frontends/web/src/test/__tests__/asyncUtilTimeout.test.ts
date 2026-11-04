import { getConfig } from '@testing-library/react'
import { describe, expect, it } from 'vitest'

// Pins the wiring from vite.config.ts ("Vitest sizing") through setup.ts to
// Testing Library. Without it findBy*/waitFor fall back to a fixed 1s, and on
// a loaded host they fail while the element is still rendering: the root of
// the LogsView / phoneLayout / ActivateTriggerDialog / ErrorsView flakes in
// the merge gate's full web lane.
describe('Testing Library wait budget', () => {
  it('follows the test timeout instead of the fixed 1s default', () => {
    const configured = Number(import.meta.env.RELIANT_ASYNC_UTIL_TIMEOUT_MS)
    expect(getConfig().asyncUtilTimeout).toBe(configured)
    // Half the test timeout, which is never under 5s — unless someone set
    // VITEST_ASYNC_UTIL_TIMEOUT_MS on purpose.
    if (!process.env.VITEST_ASYNC_UTIL_TIMEOUT_MS) expect(configured).toBeGreaterThanOrEqual(2_500)
  })
})
