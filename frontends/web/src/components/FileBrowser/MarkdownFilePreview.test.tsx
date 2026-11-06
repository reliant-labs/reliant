import { act, cleanup, fireEvent, render, screen, waitFor } from '@testing-library/react';
import { afterEach, beforeEach, describe, expect, it, vi } from 'vitest';
import { MarkdownFilePreview } from './MarkdownFilePreview';

const mocks = vi.hoisted(() => ({ openFile: vi.fn(), openLink: vi.fn(), blob: vi.fn() }));
vi.mock('../../lib/fileOpener', () => ({ useFileOpener: () => mocks.openFile }));
vi.mock('../../lib/open-link', () => ({ openLink: mocks.openLink }));
vi.mock('../../api/fileSystem', () => ({ getFilePreviewBlob: mocks.blob }));

beforeEach(() => {
  vi.resetAllMocks();
  mocks.openLink.mockResolvedValue(undefined);
  vi.stubGlobal('URL', class extends URL {
    static createObjectURL = vi.fn(() => 'blob:preview');
    static revokeObjectURL = vi.fn();
  });
});
afterEach(() => { cleanup(); vi.unstubAllGlobals(); });

function preview(content: string, filePath = '/repo/docs/readme.md', worktreeId: string | undefined = 'wt') {
  return render(<MarkdownFilePreview content={content} filePath={filePath} worktreeId={worktreeId} />);
}

describe('MarkdownFilePreview', () => {
  it('renders document GFM without chat-specific rewriting', () => {
    const { container } = preview('# Guide\n\n> A quotation\n\n- [x] Done\n- [ ] Next\n\n| Name | Value |\n| --- | --- |\n| one | two |\n\n`src/file.ts`\n\nhttps://example.com/image.png\n\n```js\nconst answer = 42;\n```');
    expect(screen.getByRole('heading', { name: 'Guide' })).toBeTruthy();
    expect(container.querySelector('blockquote')?.textContent).toContain('A quotation');
    expect(screen.getByRole('table')).toBeTruthy();
    expect(screen.getAllByRole('checkbox')).toHaveLength(2);
    expect(container.querySelector('code')?.textContent).toBe('src/file.ts');
    expect(container.querySelector('code a')).toBeNull();
    expect(container.querySelector('img')).toBeNull();
    expect(container.querySelector('pre .hljs-keyword')).toBeTruthy();
  });

  it('ignores raw HTML and blocks executable, data, file and protocol-relative URLs', () => {
    const { container } = preview('<script>alert(1)</script>\n\n<img src=x onerror=alert(1)>\n\n[bad](javascript:alert%281%29) [data](data:text/html,hi) [file](file:///etc/passwd) [remote](//example.com)\n\n![bad image](data:image/svg+xml,x)');
    expect(container.querySelector('script')).toBeNull();
    expect(container.querySelector('img')).toBeNull();
    expect(container.querySelector('a')).toBeNull();
    expect(mocks.blob).not.toHaveBeenCalled();
  });

  it('resolves relative links from the document directory and passes worktree context', () => {
    preview('[Sibling](../guide/a%20b.md#heading) [Root](/repo/main.md) [Web](https://example.com) [Mail](mailto:hi@example.com)');
    fireEvent.click(screen.getByText('Sibling'));
    expect(mocks.openFile).toHaveBeenCalledWith({ path: '/repo/guide/a b.md', fullPath: '/repo/guide/a b.md', isAbsolute: true }, 'wt');
    fireEvent.click(screen.getByText('Root'));
    expect(mocks.openFile).toHaveBeenLastCalledWith({ path: '/repo/main.md', fullPath: '/repo/main.md', isAbsolute: true }, 'wt');
    fireEvent.click(screen.getByText('Web'));
    expect(mocks.openLink).toHaveBeenCalledWith('https://example.com', 'wt');
    fireEvent.click(screen.getByText('Mail'));
    expect(mocks.openLink).toHaveBeenCalledWith('mailto:hi@example.com', 'wt');
  });

  it('keeps relative input paths relative and handles malformed links gracefully', () => {
    preview('[Sibling](../other.md) [Malformed](bad%ZZ.md)', 'docs/readme.md');
    fireEvent.click(screen.getByText('Sibling'));
    expect(mocks.openFile).toHaveBeenCalledWith({ path: 'other.md', fullPath: 'other.md', isAbsolute: false }, 'wt');
    fireEvent.click(screen.getByText('Malformed'));
    expect(screen.getByRole('status').textContent).toContain('Unable');
  });

  it('assigns stable unique heading anchors and scrolls only within its document', () => {
    preview('# Hello World\n\n# Hello World\n\n[Jump](#hello-world-1)');
    const headings = screen.getAllByRole('heading');
    expect(headings[0].id).not.toBe(headings[1].id);
    headings[1].scrollIntoView = vi.fn();
    fireEvent.click(screen.getByText('Jump'));
    expect(headings[1].scrollIntoView).toHaveBeenCalledWith({ block: 'start' });
    expect(mocks.openFile).not.toHaveBeenCalled();
  });

  it('fetches local images through the daemon and revokes blob URLs on replacement and unmount', async () => {
    mocks.blob.mockResolvedValue(new Blob(['image'], { type: 'image/png' }));
    const { rerender, unmount } = preview('![Diagram](../assets/a%20b.png)');
    await waitFor(() => expect(screen.getByRole('img').getAttribute('src')).toBe('blob:preview'));
    expect(mocks.blob).toHaveBeenCalledWith('/repo/assets/a b.png', 'wt');
    rerender(<MarkdownFilePreview content="![Next](next.png)" filePath="/repo/docs/readme.md" worktreeId="wt2" />);
    await waitFor(() => expect(mocks.blob).toHaveBeenCalledWith('/repo/docs/next.png', 'wt2'));
    await waitFor(() => expect(screen.getByRole('img').getAttribute('src')).toBe('blob:preview'));
    expect(URL.revokeObjectURL).toHaveBeenCalledTimes(1);
    unmount();
    expect(URL.revokeObjectURL).toHaveBeenCalledTimes(2);
  });

  it('ignores late image responses after unmount', async () => {
    let resolve!: (blob: Blob) => void;
    mocks.blob.mockReturnValue(new Promise<Blob>(done => { resolve = done; }));
    const { unmount } = preview('![Late](late.png)');
    unmount();
    await act(async () => { resolve(new Blob(['late'])); });
    expect(URL.createObjectURL).not.toHaveBeenCalled();
  });

  it('shows graceful fallbacks for failed images and does not guess a worktree', async () => {
    mocks.blob.mockRejectedValue(new Error('missing'));
    const { rerender } = preview('![Missing](missing.png)');
    await screen.findByText('Missing (unavailable)');
    rerender(<MarkdownFilePreview content="![No context](image.png)" filePath="readme.md" />);
    expect(screen.getByText('No context (unavailable)')).toBeTruthy();
    expect(mocks.blob).toHaveBeenCalledTimes(1);
  });

  it('handles external image load and link failures', async () => {
    mocks.openLink.mockRejectedValue(new Error('offline'));
    preview('![Remote](https://example.com/a.png)\n\n[Website](https://example.com)');
    fireEvent.error(screen.getByRole('img'));
    expect(screen.getByText('Remote (unavailable)')).toBeTruthy();
    fireEvent.click(screen.getByText('Website'));
    await screen.findByRole('status');
    expect(mocks.blob).not.toHaveBeenCalled();
  });
});
