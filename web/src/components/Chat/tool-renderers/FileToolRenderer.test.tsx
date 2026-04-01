import { render, screen, waitFor } from '@testing-library/react';
import { FileToolRenderer } from './FileToolRenderer';
import type { ToolRenderContext } from './types';

const fileSystemMocks = vi.hoisted(() => ({
  getFilePreviewInfo: vi.fn(),
}));

const fileViewerTabMock = vi.hoisted(() =>
  vi.fn(({ file, worktreeId }: { file: { name: string }, worktreeId?: string }) => (
    <div data-testid="shared-tool-preview">
      {file.name}::{worktreeId || 'no-worktree'}
    </div>
  ))
);

const lightweightDiffViewerMock = vi.hoisted(() =>
  vi.fn(({ filename, modified }: { filename?: string; modified: string }) => (
    <div data-testid="lightweight-diff-viewer">
      {filename || 'no-file'}::{modified}
    </div>
  ))
);

vi.mock('../../../api/fileSystem', () => ({
  getFilePreviewInfo: fileSystemMocks.getFilePreviewInfo,
}));

vi.mock('../../FileBrowser/FileViewerTab', () => ({
  FileViewerTab: fileViewerTabMock,
}));

vi.mock('../LightweightDiffViewer', () => ({
  LightweightDiffViewer: lightweightDiffViewerMock,
}));

function createContext(overrides: Partial<ToolRenderContext> = {}): ToolRenderContext {
  return {
    toolName: 'write',
    toolCallId: 'tool-1',
    input: {
      file_path: '/workspace/hello-world.pdf',
      content: '%PDF-1.4',
    },
    result: {
      name: 'write',
      content: 'Created hello-world.pdf',
      is_error: false,
    },
    worktreeId: 'wt-1',
    isExpanded: true,
    isCompleted: true,
    isExecuting: false,
    isPreparing: false,
    hasFailed: false,
    ...overrides,
  };
}

describe('FileToolRenderer', () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it('routes PDF write outputs through shared FileViewerTab handling', async () => {
    fileSystemMocks.getFilePreviewInfo.mockResolvedValue({
      name: 'hello-world.pdf',
      path: '/workspace/hello-world.pdf',
      size: 589,
      modified: '2026-03-09T10:51:59Z',
      viewerKind: 'pdf',
      mimeType: 'application/pdf',
      isBinary: true,
      isEditable: false,
    });

    render(<FileToolRenderer ctx={createContext()} />);

    expect(await screen.findByTestId('shared-tool-preview')).toHaveTextContent('hello-world.pdf::wt-1');
    expect(fileSystemMocks.getFilePreviewInfo).toHaveBeenCalledWith('/workspace/hello-world.pdf', 'wt-1');
    expect(lightweightDiffViewerMock).not.toHaveBeenCalled();

    await waitFor(() => {
      expect(fileViewerTabMock).toHaveBeenCalledWith(
        expect.objectContaining({
          file: expect.objectContaining({
            name: 'hello-world.pdf',
            path: '/workspace/hello-world.pdf',
            type: 'file',
          }),
          worktreeId: 'wt-1',
          embedded: true,
        }),
        undefined,
      );
    });
  });

  it('keeps text writes on the lightweight diff viewer', async () => {
    render(
      <FileToolRenderer
        ctx={createContext({
          input: {
            file_path: '/workspace/example.ts',
            content: 'const answer = 42;',
          },
        })}
      />
    );

    expect(screen.getByTestId('lightweight-diff-viewer')).toHaveTextContent('/workspace/example.ts::const answer = 42;');
    expect(fileSystemMocks.getFilePreviewInfo).not.toHaveBeenCalled();
    expect(fileViewerTabMock).not.toHaveBeenCalled();
  });
});
