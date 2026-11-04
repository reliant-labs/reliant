/**
 * A Replace All rewrites files on disk under any open editor
 * (api/fileSystem replaceInFiles → lib/fileRewrites). The editor's buffer is
 * its own state, never a cache entry a write could refetch over, so the
 * editor decides: a clean buffer reloads the new text, and a dirty one keeps
 * the user's unsaved edit.
 */
import { act, fireEvent, screen, waitFor } from "@testing-library/react";
import { beforeEach, describe, expect, it, vi } from "vitest";

import { notifyFilesRewritten } from "../../lib/fileRewrites";
import { renderWithQuery as render } from "../../test/renderWithQuery";
import { FileViewerTab } from "./FileViewerTab";
import type { FileNode } from "./index";

const mockEditorStore = {
  settings: {
    minimap: false,
    lineNumbers: true,
    wordWrap: true,
    renderWhitespace: false,
    fontSize: 13,
    tabSize: 2,
    autoSave: false,
    autoSaveDelay: 1000,
    bracketPairColorization: true,
    guides: true,
    cursorBlinking: "blink",
    cursorSmoothCaretAnimation: true,
    renderLineHighlight: "line",
    quickSuggestions: true,
    suggestOnTriggerCharacters: true,
    acceptSuggestionOnEnter: true,
    diffSideBySide: true,
    diffHideUnchanged: false,
  },
};

const mockWorktreeStore = {
  currentWorktree: { id: "wt-1", path: "/workspace", name: "main" },
  worktrees: [{ id: "wt-1", path: "/workspace", name: "main" }],
};

const fileSystemMocks = vi.hoisted(() => ({
  getFileContent: vi.fn(),
  saveFileContent: vi.fn(),
  getFilePreviewInfo: vi.fn(),
  getFilePreviewBlob: vi.fn(),
}));

// An editable stand-in for Monaco, so a test can dirty the buffer.
vi.mock("@monaco-editor/react", () => ({
  default: ({ value, onChange }: { value?: string; onChange?: (v: string) => void }) => (
    <textarea data-testid="monaco-editor" value={value} onChange={(e) => onChange?.(e.target.value)} />
  ),
}));

vi.mock("../../api/fileSystem", () => ({
  getFileContent: fileSystemMocks.getFileContent,
  saveFileContent: fileSystemMocks.saveFileContent,
  getFilePreviewInfo: fileSystemMocks.getFilePreviewInfo,
  getFilePreviewBlob: fileSystemMocks.getFilePreviewBlob,
}));

vi.mock("../../store/editorStore", () => ({
  useEditorStore: (selector: (state: typeof mockEditorStore) => unknown) => selector(mockEditorStore),
}));

vi.mock("../../hooks/worktree-queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../hooks/worktree-queries")>()),
  ...(await import("../../test/worktreeQueriesMock")).worktreeQueriesMock(() => mockWorktreeStore),
}));

vi.mock("../../lib/monacoTheme", () => ({
  getMonacoLanguage: () => "typescript",
  configureMonacoTheme: vi.fn(),
  getCurrentMonacoTheme: () => "vs-dark",
  MONACO_FONT_FAMILY: "'JetBrains Mono', monospace",
}));

vi.mock("sonner", () => ({ toast: { success: vi.fn(), error: vi.fn() } }));
vi.mock("./AddToChatPopup", () => ({ AddToChatPopup: () => null }));
vi.mock("../Chat/ImagePreviewModal", () => ({ ImagePreviewModal: () => null }));

// Generous: under a loaded machine a render round-trip can pass the 1s
// default, and these waits are for an outcome, not a deadline.
const WAIT = { timeout: 5000 };

const FILE: FileNode = {
  name: "example.ts",
  path: "/workspace/example.ts",
  type: "file",
  size: 128,
  modified: "2024-01-01T00:00:00Z",
};

function editor() {
  return screen.getByTestId("monaco-editor") as HTMLTextAreaElement;
}

async function openFile() {
  render(<FileViewerTab file={FILE} isActive />);
  await waitFor(() => expect(editor().value).toBe("old text"), WAIT);
  expect(fileSystemMocks.getFileContent).toHaveBeenCalledTimes(1);
}

describe("FileViewerTab after a Replace All", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    fileSystemMocks.getFilePreviewInfo.mockResolvedValue({
      name: "example.ts",
      path: "/workspace/example.ts",
      size: 128,
      modified: "2024-01-01T00:00:00Z",
      viewerKind: "text",
      mimeType: "text/plain",
      isBinary: false,
      isEditable: true,
    });
    fileSystemMocks.getFileContent.mockResolvedValue("old text");
  });

  it("reloads a clean buffer the replace rewrote", async () => {
    await openFile();

    fileSystemMocks.getFileContent.mockResolvedValue("new text");
    act(() => notifyFilesRewritten({ worktreeId: "wt-1", paths: ["/workspace/example.ts"] }));

    await waitFor(() => expect(editor().value).toBe("new text"), WAIT);
    expect(fileSystemMocks.getFileContent).toHaveBeenCalledTimes(2);
    // Reloaded as the saved text, so nothing reads as an unsaved change.
    expect(screen.queryByText("• Modified")).not.toBeInTheDocument();
  });

  it("leaves a dirty buffer alone", async () => {
    await openFile();
    fireEvent.change(editor(), { target: { value: "my unsaved edit" } });

    fileSystemMocks.getFileContent.mockResolvedValue("new text");
    act(() => notifyFilesRewritten({ worktreeId: "wt-1", paths: ["/workspace/example.ts"] }));
    await new Promise((r) => setTimeout(r, 20));

    expect(editor().value).toBe("my unsaved edit");
    expect(screen.getByText("• Modified")).toBeInTheDocument();
    expect(fileSystemMocks.getFileContent).toHaveBeenCalledTimes(1);
  });

  it("ignores a rewrite of another file, or of this path in another workspace", async () => {
    await openFile();

    act(() => {
      notifyFilesRewritten({ worktreeId: "wt-1", paths: ["/workspace/other.ts"] });
      notifyFilesRewritten({ worktreeId: "wt-2", paths: ["example.ts"] });
    });
    await new Promise((r) => setTimeout(r, 20));

    expect(fileSystemMocks.getFileContent).toHaveBeenCalledTimes(1);
    expect(editor().value).toBe("old text");
  });
});
