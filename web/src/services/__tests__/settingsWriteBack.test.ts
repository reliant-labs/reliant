/**
 * Opening the Appearance panel must not re-save preferences the user did not
 * touch.
 *
 * Each preference lives in a `useState` seeded from storage on mount, and an
 * effect keyed on that state writes it back. Mounting therefore fired every
 * effect once and re-persisted values nobody had changed. That looks harmless
 * until you notice the backend appends a row per save: a mount that read a
 * STALE value re-persisted it with a fresh timestamp, so the stale value became
 * the newest record and won the next load. The user-visible symptom was a font
 * size that "kept reverting to Medium" — and the database showed a perfect
 * md/lg/md/lg alternation, one pair per visit to the settings panel.
 *
 * These tests pin the guard that makes those effects idempotent.
 */
import { beforeEach, describe, expect, it, vi } from 'vitest'

const mocks = vi.hoisted(() => ({
  createSetting: vi.fn().mockResolvedValue({}),
  updateSetting: vi.fn().mockResolvedValue({}),
  batchUpsertSettings: vi.fn().mockResolvedValue([]),
  listSettings: vi.fn().mockResolvedValue({ settings: [] }),
  getToken: vi.fn().mockResolvedValue('token-abc'),
}))

vi.mock('../../api/client', () => ({
  api: {
    settings: {
      createSetting: mocks.createSetting,
      updateSetting: mocks.updateSetting,
      batchUpsertSettings: mocks.batchUpsertSettings,
      listSettings: mocks.listSettings,
    },
  },
}))

// Writes now go through settingsPersistence, which gates on a token.
vi.mock('../../api/authProvider', () => ({
  getAuthTokenProvider: () => ({ getToken: mocks.getToken }),
}))

/**
 * Settings writes are coalesced per microtask, so the RPC fires after the
 * awaited call returns. Let the queue flush before asserting on it.
 */
const flushWrites = () => new Promise((resolve) => setTimeout(resolve, 0))

/** Keys sent to the server across every batch issued so far. */
const writtenKeys = () =>
  mocks.batchUpsertSettings.mock.calls.flatMap(
    ([settings]: [Array<{ key: string; value: string }>]) => settings,
  )
vi.mock('../../lib/configReady', () => ({ waitForConfig: vi.fn().mockResolvedValue(undefined) }))
vi.mock('../../lib/logger', () => ({
  logger: { info: vi.fn(), debug: vi.fn(), warn: vi.fn(), error: vi.fn() },
}))

const { settingsSync, SETTINGS_KEYS } = await import('../settingsSync')

describe('settings write-back', () => {
  beforeEach(() => {
    localStorage.clear()
    vi.clearAllMocks()
  })

  it('does not write when the value already matches what is stored', async () => {
    localStorage.setItem(SETTINGS_KEYS.FONT_SIZE, 'lg')

    const wrote = await settingsSync.setSettingIfChanged(SETTINGS_KEYS.FONT_SIZE, 'lg')

    await flushWrites()
    expect(wrote).toBe(false)
    expect(mocks.batchUpsertSettings).not.toHaveBeenCalled()
  })

  it('writes when the value genuinely changed', async () => {
    localStorage.setItem(SETTINGS_KEYS.FONT_SIZE, 'lg')

    const wrote = await settingsSync.setSettingIfChanged(SETTINGS_KEYS.FONT_SIZE, 'xs')

    await flushWrites()
    expect(wrote).toBe(true)
    expect(writtenKeys()).toContainEqual(
      expect.objectContaining({ key: SETTINGS_KEYS.FONT_SIZE, value: 'xs' }),
    )
    expect(localStorage.getItem(SETTINGS_KEYS.FONT_SIZE)).toBe('xs')
  })

  it('survives repeated mounts without a single redundant write', async () => {
    // The regression: five visits to the settings panel used to mean five
    // saves of an untouched preference, any one of which could enshrine a
    // stale read as the newest record.
    localStorage.setItem(SETTINGS_KEYS.FONT_SIZE, 'lg')

    for (let visit = 0; visit < 5; visit++) {
      await settingsSync.setSettingIfChanged(SETTINGS_KEYS.FONT_SIZE, 'lg')
    }

    await flushWrites()
    expect(mocks.batchUpsertSettings).not.toHaveBeenCalled()
    expect(localStorage.getItem(SETTINGS_KEYS.FONT_SIZE)).toBe('lg')
  })

  it('persists a real change exactly once per distinct value', async () => {
    localStorage.setItem(SETTINGS_KEYS.FONT_SIZE, 'md')

    // User picks Large, then the panel remounts twice.
    await settingsSync.setSettingIfChanged(SETTINGS_KEYS.FONT_SIZE, 'lg')
    await settingsSync.setSettingIfChanged(SETTINGS_KEYS.FONT_SIZE, 'lg')
    await settingsSync.setSettingIfChanged(SETTINGS_KEYS.FONT_SIZE, 'lg')

    await flushWrites()
    // Exactly one value reaches the server, however many times the panel
    // remounted — the point of the idempotence guard.
    expect(writtenKeys()).toEqual([
      expect.objectContaining({ key: SETTINGS_KEYS.FONT_SIZE, value: 'lg' }),
    ])
    expect(localStorage.getItem(SETTINGS_KEYS.FONT_SIZE)).toBe('lg')
  })

  it('sends one upsert rather than create-then-update', async () => {
    // The old path tried createSetting and fell back to updateSetting on an
    // ALREADY_EXISTS conflict that the server could never report, because the
    // uniqueness constraint did not apply to user-level settings. createSetting
    // is now an upsert server-side, so one call is the whole story.
    await settingsSync.setSetting(SETTINGS_KEYS.FONT_SIZE, 'xl')

    await flushWrites()
    expect(mocks.batchUpsertSettings).toHaveBeenCalledTimes(1)
    expect(mocks.createSetting).not.toHaveBeenCalled()
    expect(mocks.updateSetting).not.toHaveBeenCalled()
  })
})
