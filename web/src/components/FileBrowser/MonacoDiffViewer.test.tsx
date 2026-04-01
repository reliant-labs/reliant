import { render, screen, waitFor } from "@testing-library/react";
import { MonacoDiffViewer } from "./MonacoDiffViewer";
import type { FileChange } from "../Chat/RecentChanges";
import { FileChangeStatus } from "../../gen/reliant/v1/common_pb";

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
};

const fileSystemMocks = vi.hoisted(() => ({
  getFilePreviewInfo: vi.fn(),
}));

const monacoManagerMocks = vi.hoisted(() => ({
  getMonaco: vi.fn(),
}));

const fileViewerTabMock = vi.hoisted(() => vi.fn(({ file, worktreeId }: { file: { name: string }; worktreeId?: string }) => (
  <div data-testid="shared-file-preview">
    {file.name}::{worktreeId || "no-worktree"}
  </div>
)));

vi.mock("../../api/fileSystem", () => ({
  getFilePreviewInfo: fileSystemMocks.getFilePreviewInfo,
}));

vi.mock("../../store/editorStore", () => ({
  useEditorStore: (selector: (state: typeof mockEditorStore) => unknown) => selector(mockEditorStore),
}));

vi.mock("../../store/worktreeStore", () => ({
  useWorktreeStore: (selector: (state: typeof mockWorktreeStore) => unknown) => selector(mockWorktreeStore),
}));

vi.mock("../../lib/monacoTheme", () => ({
  getMonacoLanguage: () => "plaintext",
  configureMonacoTheme: vi.fn(),
  getCurrentMonacoTheme: () => "vs-dark",
}));

vi.mock("../../lib/monacoManager", () => ({
  monacoManager: monacoManagerMocks,
}));

vi.mock("./FileViewerTab", () => ({
  FileViewerTab: fileViewerTabMock,
}));

function createFileChange(overrides: Partial<FileChange> = {}): FileChange {
  return {
    path: "hello-world.pdf",
    status: FileChangeStatus.UNTRACKED,
    diff: "[Binary file]",
    content: "",
    original_content: "",
    is_new: true,
    ...overrides,
  };
}

describe("MonacoDiffViewer", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("routes PDF change files through shared FileViewerTab handling", async () => {
    fileSystemMocks.getFilePreviewInfo.mockResolvedValue({
      name: "hello-world.pdf",
      path: "hello-world.pdf",
      size: 589,
      modified: "2026-03-09T10:51:59Z",
      viewerKind: "pdf",
      mimeType: "application/pdf",
      isBinary: true,
      isEditable: false,
    });

    render(<MonacoDiffViewer file={createFileChange()} />);

    expect(await screen.findByTestId("shared-file-preview")).toHaveTextContent("hello-world.pdf::wt-1");
    expect(fileSystemMocks.getFilePreviewInfo).toHaveBeenCalledWith("hello-world.pdf", "wt-1");
    expect(monacoManagerMocks.getMonaco).not.toHaveBeenCalled();

    await waitFor(() => {
      expect(fileViewerTabMock).toHaveBeenCalledWith(
        expect.objectContaining({
          file: expect.objectContaining({
            name: "hello-world.pdf",
            path: "hello-world.pdf",
            type: "file",
          }),
          worktreeId: "wt-1",
        }),
        undefined,
      );
    });
  });
});
