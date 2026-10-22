import { beforeEach, describe, expect, it, vi } from 'vitest';

import {
  upsertStringSetting,
  readSetting,
  deleteSettingIfExists,
} from '../settingsPersistence';

const mocks = vi.hoisted(() => ({
  batchUpsertSettings: vi.fn(),
  createSetting: vi.fn(),
  updateSetting: vi.fn(),
  getSetting: vi.fn(),
  deleteSetting: vi.fn(),
  listSettings: vi.fn(),
  getToken: vi.fn(),
  initialize: vi.fn(),
  isInitialized: vi.fn(),
  getSettingFromCache: vi.fn(),
  updateCache: vi.fn(),
  removeFromCache: vi.fn(),
}));

vi.mock('../../api/client', () => ({
  api: {
    settings: {
      batchUpsertSettings: mocks.batchUpsertSettings,
      createSetting: mocks.createSetting,
      updateSetting: mocks.updateSetting,
      getSetting: mocks.getSetting,
      deleteSetting: mocks.deleteSetting,
      listSettings: mocks.listSettings,
    },
  },
}));

vi.mock('../../api/authProvider', () => ({
  getAuthTokenProvider: () => ({ getToken: mocks.getToken }),
}));

vi.mock('../../services/settingsSync', () => ({
  settingsSync: {
    initialize: mocks.initialize,
    isInitialized: mocks.isInitialized,
    getSettingFromCache: mocks.getSettingFromCache,
    updateCache: mocks.updateCache,
    removeFromCache: mocks.removeFromCache,
  },
}));

describe('settingsPersistence batching and auth gating', () => {
  beforeEach(() => {
    for (const mock of Object.values(mocks)) mock.mockReset();
    // Signed in by default.
    mocks.getToken.mockResolvedValue('token-abc');
    mocks.batchUpsertSettings.mockResolvedValue([]);
    mocks.isInitialized.mockReturnValue(true);
    mocks.getSettingFromCache.mockReturnValue(null);
    mocks.initialize.mockResolvedValue(undefined);
  });

  it('collapses N concurrent key writes into ONE request', async () => {
    // The tour saves three keys per step transition; the tool-call panel one
    // per category. Each used to be its own UpdateSetting/CreateSetting.
    await Promise.all([
      upsertStringSetting('tour.completed', 'true'),
      upsertStringSetting('tour.completedSteps', '["a","b"]'),
      upsertStringSetting('tour.skippedSteps', '[]'),
      upsertStringSetting('toolcalls.collapseDefaults', '{"mcp":true}'),
      upsertStringSetting('appearance.theme', 'dark'),
    ]);

    expect(mocks.batchUpsertSettings).toHaveBeenCalledTimes(1);
    // The per-key write RPCs are gone from this path entirely.
    expect(mocks.createSetting).not.toHaveBeenCalled();
    expect(mocks.updateSetting).not.toHaveBeenCalled();

    const written = mocks.batchUpsertSettings.mock.calls[0][0];
    expect(written).toHaveLength(5);
    expect(written.map((s: { key: string }) => s.key).sort()).toEqual([
      'appearance.theme',
      'toolcalls.collapseDefaults',
      'tour.completed',
      'tour.completedSteps',
      'tour.skippedSteps',
    ]);
  });

  it('keeps only the last value when a key is written twice in one window', async () => {
    await Promise.all([
      upsertStringSetting('appearance.fontSize', '14'),
      upsertStringSetting('appearance.fontSize', '16'),
    ]);

    expect(mocks.batchUpsertSettings).toHaveBeenCalledTimes(1);
    const written = mocks.batchUpsertSettings.mock.calls[0][0];
    expect(written).toHaveLength(1);
    expect(written[0]).toMatchObject({ key: 'appearance.fontSize', value: '16' });
  });

  it('issues ZERO write requests while unauthenticated', async () => {
    mocks.getToken.mockResolvedValue(null);

    await Promise.all([
      upsertStringSetting('tour.completed', 'true'),
      upsertStringSetting('tour.completedSteps', '[]'),
    ]);

    // This is the 401-storm fix: doomed calls are never issued.
    expect(mocks.batchUpsertSettings).not.toHaveBeenCalled();
    expect(mocks.createSetting).not.toHaveBeenCalled();
    expect(mocks.updateSetting).not.toHaveBeenCalled();
    // The in-memory cache is still updated so the UI stays consistent.
    expect(mocks.updateCache).toHaveBeenCalledTimes(2);
  });

  it('issues ZERO delete requests while unauthenticated', async () => {
    mocks.getToken.mockResolvedValue(null);

    await deleteSettingIfExists('tour.currentStep');

    expect(mocks.deleteSetting).not.toHaveBeenCalled();
    expect(mocks.removeFromCache).toHaveBeenCalledWith('tour.currentStep');
  });

  it('resolves writers even when the batch request fails', async () => {
    mocks.batchUpsertSettings.mockRejectedValue(new Error('boom'));

    // A rejected flush must not leave callers hanging forever.
    await expect(
      Promise.all([
        upsertStringSetting('a', '1'),
        upsertStringSetting('b', '2'),
      ]),
    ).resolves.toBeDefined();
  });

  it('reads N keys with ZERO per-key GetSetting calls', async () => {
    mocks.isInitialized.mockReturnValue(true);
    mocks.getSettingFromCache.mockImplementation((key: string) =>
      key === 'tour.completed' ? { value: 'true' } : null,
    );

    const results = await Promise.all([
      readSetting('tour.completed'),
      readSetting('tour.completedSteps'),
      readSetting('tour.skippedSteps'),
      readSetting('notifications.enabled'),
      readSetting('notifications.soundEnabled'),
    ]);

    // Served entirely from the ListSettings-populated cache.
    expect(mocks.getSetting).not.toHaveBeenCalled();
    expect(results[0]).toEqual({ status: 'found', value: 'true' });
    expect(results[1]).toEqual({ status: 'missing' });
  });

  it('hydrates via a single initialize() instead of per-key GetSetting when cache is cold', async () => {
    let ready = false;
    mocks.isInitialized.mockImplementation(() => ready);
    mocks.initialize.mockImplementation(async () => {
      ready = true;
    });
    mocks.getSettingFromCache.mockImplementation((key: string) =>
      ready && key === 'a' ? { value: 'cached' } : null,
    );

    const results = await Promise.all([readSetting('a'), readSetting('b')]);

    // No per-key GetSetting fallback — that was 36 RPCs in the measured log.
    expect(mocks.getSetting).not.toHaveBeenCalled();
    expect(results[0]).toEqual({ status: 'found', value: 'cached' });
  });

  it('does not attempt hydration while unauthenticated', async () => {
    mocks.isInitialized.mockReturnValue(false);
    mocks.getToken.mockResolvedValue(null);

    const result = await readSetting('tour.completed');

    expect(mocks.initialize).not.toHaveBeenCalled();
    expect(mocks.getSetting).not.toHaveBeenCalled();
    expect(result).toEqual({ status: 'error' });
  });
});
