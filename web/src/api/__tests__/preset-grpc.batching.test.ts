import { beforeEach, describe, expect, it, vi } from 'vitest';

import { presetGrpc } from '../preset-grpc';

const mocks = vi.hoisted(() => ({
  getDefaultPreset: vi.fn(),
  getDefaultPresetsBatch: vi.fn(),
}));

vi.mock('../grpc-client', () => ({
  grpcClient: {
    preset: () => ({
      getDefaultPreset: mocks.getDefaultPreset,
      getDefaultPresetsBatch: mocks.getDefaultPresetsBatch,
    }),
  },
}));

// singleflight keys by args and is module-global; a stale in-flight entry from
// a previous test would mask a real regression here.
vi.mock('../../lib/singleflight', () => ({
  singleflight: <T,>(_key: string, fn: () => Promise<T>) => fn(),
}));

describe('preset-grpc default-preset batching', () => {
  beforeEach(() => {
    mocks.getDefaultPreset.mockReset();
    mocks.getDefaultPresetsBatch.mockReset();
    mocks.getDefaultPresetsBatch.mockResolvedValue({ presetsByWorkflow: {} });
  });

  it('collapses N concurrent per-workflow lookups into ONE batch request', async () => {
    mocks.getDefaultPresetsBatch.mockResolvedValue({
      presetsByWorkflow: {
        'agent-a': { presets: { '': 'researcher' } },
        'agent-b': { presets: { Critic: 'careful' } },
        // agent-c intentionally absent: a workflow with no defaults is omitted.
      },
    });

    // Six different agents on screen, all asking in the same tick.
    const workflows = ['agent-a', 'agent-b', 'agent-c', 'agent-d', 'agent-e', 'agent-f'];
    const results = await Promise.all(
      workflows.map((name) => presetGrpc.getDefaultPresets('project-1', name)),
    );

    // The whole point: one request, not six.
    expect(mocks.getDefaultPresetsBatch).toHaveBeenCalledTimes(1);
    // And the per-workflow RPC is not used at all any more.
    expect(mocks.getDefaultPreset).not.toHaveBeenCalled();

    const request = mocks.getDefaultPresetsBatch.mock.calls[0][0];
    expect(request.projectId).toBe('project-1');
    expect([...request.workflowNames].sort()).toEqual([...workflows].sort());

    // Each caller still gets exactly its own workflow's defaults back.
    expect(results[0]).toEqual({ '': 'researcher' });
    expect(results[1]).toEqual({ Critic: 'careful' });
    // Omitted from the response => "no defaults", same as the single RPC gave.
    expect(results[2]).toEqual({});
  });

  it('deduplicates repeated workflow names within one batch', async () => {
    await Promise.all([
      presetGrpc.getDefaultPresets('project-1', 'agent-a'),
      presetGrpc.getDefaultPresets('project-1', 'agent-a'),
      presetGrpc.getDefaultPresets('project-1', 'agent-b'),
    ]);

    expect(mocks.getDefaultPresetsBatch).toHaveBeenCalledTimes(1);
    expect([...mocks.getDefaultPresetsBatch.mock.calls[0][0].workflowNames].sort()).toEqual([
      'agent-a',
      'agent-b',
    ]);
  });

  it('keeps separate projects in separate batches', async () => {
    await Promise.all([
      presetGrpc.getDefaultPresets('project-1', 'agent-a'),
      presetGrpc.getDefaultPresets('project-2', 'agent-a'),
    ]);

    expect(mocks.getDefaultPresetsBatch).toHaveBeenCalledTimes(2);
  });

  it('resolves every caller with {} when the batch request fails', async () => {
    mocks.getDefaultPresetsBatch.mockRejectedValue(new Error('network down'));

    const results = await Promise.all([
      presetGrpc.getDefaultPresets('project-1', 'agent-a'),
      presetGrpc.getDefaultPresets('project-1', 'agent-b'),
    ]);

    // Graceful degradation: callers treat presets as absent, never throw.
    expect(results).toEqual([{}, {}]);
  });

  it('getDefaultPresetsBatch issues a single request for an explicit list', async () => {
    await presetGrpc.getDefaultPresetsBatch('project-1', ['a', 'b', 'c', 'd']);

    expect(mocks.getDefaultPresetsBatch).toHaveBeenCalledTimes(1);
    expect(mocks.getDefaultPresetsBatch.mock.calls[0][0].workflowNames).toEqual([
      'a',
      'b',
      'c',
      'd',
    ]);
  });

  it('sends no request at all for an empty workflow list', async () => {
    const result = await presetGrpc.getDefaultPresetsBatch('project-1', []);

    expect(result).toEqual({});
    expect(mocks.getDefaultPresetsBatch).not.toHaveBeenCalled();
  });
});
