import { create } from '@bufbuild/protobuf';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';

const getDaemon = vi.fn();

vi.mock('../../services/controlPlane/environments', () => ({
  getDaemon: (id: string) => getDaemon(id),
}));

import { DaemonInfoSchema, DaemonStatus, ListDaemonsResponseSchema } from '../../gen/reliant/v1/daemon_registry_pb';
import { queryClient } from '../../lib/query-client';
import { DAEMON_LIST_QUERY_KEY } from '../daemon-registry-queries';
import { readActiveDaemonPreviewTemplate } from '../useActiveDaemonPreviewTemplate';

const TEMPLATE = 'https://{port}-d1.workspaces.example.com';

function seedDaemons(...daemons: { daemonId: string; daemonType: string; status: DaemonStatus }[]) {
  queryClient.setQueryData(
    DAEMON_LIST_QUERY_KEY,
    create(ListDaemonsResponseSchema, { daemons: daemons.map((d) => create(DaemonInfoSchema, d)) }),
  );
}

beforeEach(() => {
  getDaemon.mockReset();
  getDaemon.mockResolvedValue({ daemon: undefined, workspaceBaseDomain: TEMPLATE });
});

afterEach(() => {
  queryClient.clear();
});

describe('readActiveDaemonPreviewTemplate', () => {
  it("is the active cloud daemon's template", async () => {
    seedDaemons(
      { daemonId: 'd0', daemonType: 'managed', status: DaemonStatus.DISCONNECTED },
      { daemonId: 'd1', daemonType: 'managed', status: DaemonStatus.ACTIVE },
    );
    expect(await readActiveDaemonPreviewTemplate()).toBe(TEMPLATE);
    expect(getDaemon).toHaveBeenCalledWith('d1');
  });

  it('is undefined for a self-hosted daemon, whose localhost is this machine', async () => {
    seedDaemons({ daemonId: 'd1', daemonType: 'self_hosted', status: DaemonStatus.ACTIVE });
    expect(await readActiveDaemonPreviewTemplate()).toBeUndefined();
    expect(getDaemon).not.toHaveBeenCalled();
  });

  it('is undefined when the control plane cannot say', async () => {
    seedDaemons({ daemonId: 'd1', daemonType: 'managed', status: DaemonStatus.ACTIVE });
    getDaemon.mockRejectedValue(new Error('unavailable'));
    expect(await readActiveDaemonPreviewTemplate()).toBeUndefined();
  });

  it('is undefined before the daemon list is loaded', async () => {
    expect(await readActiveDaemonPreviewTemplate()).toBeUndefined();
  });
});
