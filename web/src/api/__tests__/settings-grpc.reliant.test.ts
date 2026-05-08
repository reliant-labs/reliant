import { describe, expect, it, beforeEach, vi } from 'vitest';
import { settingsGrpc } from '../settings-grpc';

const mocks = vi.hoisted(() => ({
  syncReliantProvider: vi.fn(),
}));

vi.mock('../grpc-client', () => ({
  grpcClient: {
    settings: () => ({
      syncReliantProvider: mocks.syncReliantProvider,
    }),
  },
}));

describe('settings-grpc Reliant provider mappings', () => {
  beforeEach(() => {
    mocks.syncReliantProvider.mockReset();
  });

  it('syncReliantProvider maps request and response payloads', async () => {
    mocks.syncReliantProvider.mockResolvedValue({
      success: true,
      message: 'synced',
      synced: true,
      createdOrg: true,
      createdKey: true,
      rotatedKey: false,
      provider: {
        provider: 'reliant',
        configured: true,
        hasApiKey: true,
        maskedKey: 'sk-a...1234',
        displayName: 'Reliant',
      },
    });

    const result = await settingsGrpc.syncReliantProvider(true);

    expect(result).toEqual({
      success: true,
      message: 'synced',
      synced: true,
      created_org: true,
      created_key: true,
      rotated_key: false,
      provider: {
        provider: 'reliant',
        configured: true,
        has_api_key: true,
        masked_key: 'sk-a...1234',
        display_name: 'Reliant',
      },
    });

    const request = mocks.syncReliantProvider.mock.calls[0][0];
    expect(request.forceRotate).toBe(true);
  });
});
