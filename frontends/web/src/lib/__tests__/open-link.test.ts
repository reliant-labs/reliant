/**
 * Tests for openLink routing.
 *
 * Dev URLs open in the embedded browser — and that path has to end at a
 * viewer, not just a browser tab: the tab list is data, and nothing renders it
 * until a browser viewer exists in viewerStore. Every other link opens in the
 * default browser.
 */

import { describe, it, expect, vi, beforeEach } from 'vitest';

const createTab = vi.fn(async () => 'browser-tab-1');
const openBrowserViewer = vi.fn(async () => {});
const openExternal = vi.fn(async (_url: string) => {});
const readTemplate = vi.fn(async (): Promise<string | undefined> => undefined);
const signedInPreviewUrl = vi.fn(async (url: string) => `${new URL(url).origin}/__proxy/callback?token=t`);

let mockBrowserState: Record<string, unknown>;
let mockViewerState: Record<string, unknown>;
let mockProjectState: Record<string, unknown>;
let electron = true;

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

vi.mock('../../hooks/useActiveDaemonPreviewTemplate', () => ({
  readActiveDaemonPreviewTemplate: () => readTemplate(),
}));

vi.mock('../proxy-session', () => ({
  signedInPreviewUrl: (url: string) => signedInPreviewUrl(url),
}));

vi.mock('../constants', async (importOriginal) => ({
  ...(await importOriginal<typeof import('../constants')>()),
  isElectron: () => electron,
}));

import { openLink } from '../open-link';

const TEMPLATE = 'https://{port}-d1.workspaces.example.com';

beforeEach(() => {
  vi.clearAllMocks();
  electron = true;
  readTemplate.mockResolvedValue(undefined);
  mockBrowserState = { createTab };
  mockViewerState = { openBrowserViewer };
  mockProjectState = { currentProject: { id: 'project-1' } };
  (window as unknown as { electronAPI?: unknown }).electronAPI = {
    openExternal: (url: string) => openExternal(url),
  };
});

describe('openLink routing', () => {
  it('opens a localhost link in a browser viewer so the new tab is actually rendered', async () => {
    await openLink('http://localhost:5173/foo', 'worktree-1');

    expect(createTab).toHaveBeenCalledWith('worktree-1', 'http://localhost:5173/foo', 'project-1');
    expect(openBrowserViewer).toHaveBeenCalledWith('project-1', 'worktree-1', 'browser-tab-1');
    expect(openExternal).not.toHaveBeenCalled();
    expect(signedInPreviewUrl).not.toHaveBeenCalled();
  });

  it('opens a non-dev link in the default browser', async () => {
    await openLink('https://github.com/reliant-labs/reliant/pull/1', 'worktree-1');

    expect(createTab).not.toHaveBeenCalled();
    expect(openExternal).toHaveBeenCalledWith('https://github.com/reliant-labs/reliant/pull/1');
  });

  it('sends localhost to the cloud daemon, already signed in to the workspace-proxy', async () => {
    readTemplate.mockResolvedValue(TEMPLATE);

    await openLink('http://localhost:5173/app?x=1', 'worktree-1');

    expect(signedInPreviewUrl).toHaveBeenCalledWith('https://5173-d1.workspaces.example.com/app?x=1');
    expect(createTab).toHaveBeenCalledWith(
      'worktree-1',
      'https://5173-d1.workspaces.example.com/__proxy/callback?token=t',
      'project-1',
    );
    expect(openExternal).not.toHaveBeenCalled();
  });

  it("opens the cloud daemon's preview URL embedded", async () => {
    readTemplate.mockResolvedValue(TEMPLATE);

    await openLink('https://3000-d1.workspaces.example.com/', 'worktree-1');

    expect(signedInPreviewUrl).toHaveBeenCalledWith('https://3000-d1.workspaces.example.com/');
    expect(createTab).toHaveBeenCalled();
    expect(openExternal).not.toHaveBeenCalled();
  });

  it('opens a dev URL in a new tab in the web build, mapped to the cloud daemon', async () => {
    electron = false;
    readTemplate.mockResolvedValue(TEMPLATE);

    await openLink('http://localhost:5173/', 'worktree-1');

    expect(createTab).not.toHaveBeenCalled();
    expect(openExternal).toHaveBeenCalledWith('https://5173-d1.workspaces.example.com/');
  });

  it('falls back to the system browser when there is no current project', async () => {
    mockProjectState = { currentProject: null };

    await openLink('http://localhost:5173/', 'worktree-1');

    expect(openBrowserViewer).not.toHaveBeenCalled();
    expect(openExternal).toHaveBeenCalledWith('http://localhost:5173/');
  });

  it('falls back to the system browser when opening the viewer fails', async () => {
    openBrowserViewer.mockRejectedValueOnce(new Error('viewer blew up'));

    await openLink('http://localhost:5173/', 'worktree-1');

    expect(openExternal).toHaveBeenCalledWith('http://localhost:5173/');
  });
});
