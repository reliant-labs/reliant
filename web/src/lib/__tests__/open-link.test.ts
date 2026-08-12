/**
 * Tests for openLink routing.
 *
 * The in-app path has to end at a viewer, not just a browser tab: the tab list
 * is data, and nothing renders it until a browser viewer exists in viewerStore.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';

const createTab = vi.fn(async () => 'browser-tab-1');
const openBrowserViewer = vi.fn(async () => {});
const openExternal = vi.fn(async () => {});
const getPreferences = vi.fn(async () => ({
  additional: {} as Record<string, string>,
}));

let mockBrowserState: Record<string, unknown>;
let mockViewerState: Record<string, unknown>;
let mockProjectState: Record<string, unknown>;

vi.mock('../../store/browserStore', () => ({
  useBrowserStore: {
    getState: () => mockBrowserState,
  },
}));

vi.mock('../../store/viewerStore', () => ({
  useViewerStore: {
    getState: () => mockViewerState,
  },
}));

vi.mock('../../store/projectStore', () => ({
  useProjectStore: {
    getState: () => mockProjectState,
  },
}));

vi.mock('../../api/client', () => ({
  api: {
    settings: {
      getPreferences: () => getPreferences(),
    },
  },
}));

vi.mock('../constants', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../constants')>()),
  isElectron: () => true,
}));

import { openLink } from '../open-link';

beforeEach(() => {
  vi.clearAllMocks();
  mockBrowserState = { createTab };
  mockViewerState = { openBrowserViewer };
  mockProjectState = { currentProject: { id: 'project-1' } };
  getPreferences.mockResolvedValue({ additional: {} });
  (window as unknown as { electronAPI?: unknown }).electronAPI = {
    openExternal: (url: string) => openExternal(url),
  };
});

describe('openLink in-app routing', () => {
  it('opens a browser viewer so the new tab is actually rendered', async () => {
    await openLink('https://example.com/foo', 'worktree-1');

    expect(createTab).toHaveBeenCalledWith(
      'worktree-1',
      'https://example.com/foo',
      'project-1',
    );
    expect(openBrowserViewer).toHaveBeenCalledWith(
      'project-1',
      'worktree-1',
      'browser-tab-1',
    );
    expect(openExternal).not.toHaveBeenCalled();
  });

  it('falls back to the system browser when there is no current project', async () => {
    mockProjectState = { currentProject: null };

    await openLink('https://example.com/foo', 'worktree-1');

    expect(openBrowserViewer).not.toHaveBeenCalled();
    expect(openExternal).toHaveBeenCalledWith('https://example.com/foo');
  });

  it('honors the system-browser preference', async () => {
    getPreferences.mockResolvedValue({
      additional: { browserOpenLinksInApp: 'false' },
    });

    await openLink('https://example.com/foo', 'worktree-1');

    expect(openBrowserViewer).not.toHaveBeenCalled();
    expect(openExternal).toHaveBeenCalledWith('https://example.com/foo');
  });

  it('falls back to the system browser when opening the viewer fails', async () => {
    openBrowserViewer.mockRejectedValueOnce(new Error('viewer blew up'));

    await openLink('https://example.com/foo', 'worktree-1');

    expect(openExternal).toHaveBeenCalledWith('https://example.com/foo');
  });
});
