import { fireEvent, screen, waitFor, within } from "@testing-library/react";
// FileViewerTab shows the shared machine-wait state, which reads daemon status
// through React Query — so it needs a provider the way the real app has one.
import { renderWithQuery as render } from "../../test/renderWithQuery";
import { afterAll, beforeAll } from "vitest";
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

vi.mock("@monaco-editor/react", () => ({
  default: ({ value, options, onChange }: {
    value?: string;
    options?: { readOnly?: boolean };
    onChange?: (value: string) => void;
  }) => (
    <textarea
      className="monaco-editor"
      aria-label="File source"
      data-testid="monaco-editor"
      data-readonly={options?.readOnly ? "true" : "false"}
      readOnly={options?.readOnly}
      value={value}
      onChange={(event) => onChange?.(event.target.value)}
    />
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

vi.mock("../../store/worktreeStore", () => ({
  useWorktreeStore: (selector: (state: typeof mockWorktreeStore) => unknown) => selector(mockWorktreeStore),
}));
// Workspace rows are read through hooks/worktree-queries; answer them from the
// same fixture.
vi.mock("../../hooks/worktree-queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../hooks/worktree-queries")>()),
  ...(await import("../../test/worktreeQueriesMock")).worktreeQueriesMock(() => (mockWorktreeStore)),
}));

vi.mock("../../lib/monacoTheme", () => ({
  getMonacoLanguage: () => "typescript",
  configureMonacoTheme: vi.fn(),
  getCurrentMonacoTheme: () => "vs-dark",
  MONACO_FONT_FAMILY: "'JetBrains Mono', monospace",
}));

vi.mock("sonner", () => ({
  toast: {
    success: vi.fn(),
    error: vi.fn(),
  },
}));

vi.mock("./AddToChatPopup", () => ({
  AddToChatPopup: () => null,
}));

vi.mock("../Chat/ImagePreviewModal", () => ({
  ImagePreviewModal: ({ isOpen }: { isOpen: boolean }) =>
    isOpen ? <div data-testid="image-preview-modal" /> : null,
}));

function createFile(overrides: Partial<FileNode> = {}): FileNode {
  return {
    name: "example.ts",
    path: "/workspace/example.ts",
    type: "file",
    size: 128,
    modified: "2024-01-01T00:00:00Z",
    ...overrides,
  };
}

describe("FileViewerTab", () => {
  const originalCreateObjectURL = URL.createObjectURL;
  const originalRevokeObjectURL = URL.revokeObjectURL;
  let createObjectURLSpy: ReturnType<typeof vi.fn>;
  let revokeObjectURLSpy: ReturnType<typeof vi.fn>;

  beforeAll(() => {
    createObjectURLSpy = vi.fn(() => "blob:preview-url");
    revokeObjectURLSpy = vi.fn();

    Object.defineProperty(URL, "createObjectURL", {
      configurable: true,
      writable: true,
      value: createObjectURLSpy,
    });
    Object.defineProperty(URL, "revokeObjectURL", {
      configurable: true,
      writable: true,
      value: revokeObjectURLSpy,
    });
  });

  afterAll(() => {
    Object.defineProperty(URL, "createObjectURL", {
      configurable: true,
      writable: true,
      value: originalCreateObjectURL,
    });
    Object.defineProperty(URL, "revokeObjectURL", {
      configurable: true,
      writable: true,
      value: originalRevokeObjectURL,
    });
  });

  beforeEach(() => {
    vi.clearAllMocks();
    createObjectURLSpy.mockReturnValue("blob:preview-url");
    revokeObjectURLSpy.mockImplementation(() => {});
  });

  describe("Markdown view mode", () => {
    function mockMarkdown(name = "README.md", viewerKind = "text") {
      const file = createFile({ name, path: `/workspace/${name}` });
      fileSystemMocks.getFilePreviewInfo.mockResolvedValue({
        ...file,
        viewerKind,
        mimeType: "text/markdown",
        isBinary: viewerKind !== "text",
        isEditable: viewerKind === "text",
      });
      fileSystemMocks.getFileContent.mockResolvedValue("# Saved heading");
      fileSystemMocks.getFilePreviewBlob.mockResolvedValue(new Blob(["media"]));
      fileSystemMocks.saveFileContent.mockResolvedValue(undefined);
      return file;
    }

    it.each(["README.md", "README.MD", "guide.markdown", "guide.MaRkDoWn"])(
      "offers accessible source-default controls for %s",
      async (name) => {
        render(<FileViewerTab file={mockMarkdown(name)} isActive />);
        expect(await screen.findByTestId("monaco-editor")).toHaveValue("# Saved heading");
        const controls = screen.getByRole("group", { name: "Markdown view mode" });
        expect(within(controls).getByRole("button", { name: "Markdown", pressed: true })).toBeInTheDocument();
        expect(within(controls).getByRole("button", { name: "Preview", pressed: false })).toBeInTheDocument();
        expect(screen.queryByRole("heading", { name: "Saved heading" })).not.toBeInTheDocument();
      },
    );

    it.each([
      ["notes.txt", "text"],
      ["README.mdx", "text"],
      ["README.md.txt", "text"],
      ["README.md", "pdf"],
      ["README.md", "binary"],
      ["README.md", "image"],
      ["README.md", "audio"],
      ["README.md", "video"],
    ])("does not offer Markdown controls for %s classified as %s", async (name, kind) => {
      const { container } = render(<FileViewerTab file={mockMarkdown(name, kind)} isActive />);
      await waitFor(() => expect(container).toHaveTextContent("text/markdown"));
      expect(screen.queryByRole("button", { name: "Markdown" })).not.toBeInTheDocument();
      expect(screen.queryByRole("button", { name: "Preview" })).not.toBeInTheDocument();
    });

    it("renders the unsaved buffer without saving or refetching and keeps the same editor mounted", async () => {
      render(<FileViewerTab file={mockMarkdown()} isActive />);
      const editor = await screen.findByTestId("monaco-editor");
      fireEvent.change(editor, { target: { value: "# Unsaved heading\n\n**Draft text**" } });
      fireEvent.click(screen.getByRole("button", { name: "Preview" }));
      expect(await screen.findByRole("heading", { name: "Unsaved heading", level: 1 })).toBeInTheDocument();
      expect(screen.getByText("Draft text").tagName).toBe("STRONG");
      expect(screen.queryByRole("heading", { name: "Saved heading" })).not.toBeInTheDocument();
      expect(screen.getByRole("button", { name: "Preview", pressed: true })).toBeInTheDocument();
      expect(screen.getByRole("button", { name: "Markdown", pressed: false })).toBeInTheDocument();
      expect(screen.getByText("• Modified")).toBeInTheDocument();
      expect(screen.getByTestId("monaco-editor")).toBe(editor);
      fireEvent.click(screen.getByRole("button", { name: "Markdown" }));
      expect(screen.getByTestId("monaco-editor")).toBe(editor);
      expect(editor).toHaveValue("# Unsaved heading\n\n**Draft text**");
      expect(fileSystemMocks.getFileContent).toHaveBeenCalledTimes(1);
      expect(fileSystemMocks.getFilePreviewInfo).toHaveBeenCalledTimes(1);
      expect(fileSystemMocks.saveFileContent).not.toHaveBeenCalled();
    });

    it.each(["button", "shortcut"])("saves the dirty buffer from preview using the %s", async (method) => {
      render(<FileViewerTab file={mockMarkdown()} isActive />);
      fireEvent.change(await screen.findByTestId("monaco-editor"), { target: { value: "# Draft to save" } });
      fireEvent.click(screen.getByRole("button", { name: "Preview" }));
      expect(await screen.findByRole("heading", { name: "Draft to save" })).toBeInTheDocument();
      expect(screen.getByText("• Modified")).toBeInTheDocument();
      if (method === "button") {
        fireEvent.click(screen.getByRole("button", { name: "Save (Cmd+S / Ctrl+S)" }));
      } else {
        screen.getByLabelText("Markdown preview", { selector: "[tabindex]" }).focus();
        fireEvent.keyDown(document.activeElement!, { key: "s", ctrlKey: true });
      }
      await waitFor(() => expect(fileSystemMocks.saveFileContent).toHaveBeenCalledWith("/workspace/README.md", "# Draft to save", "wt-1"));
      await waitFor(() => expect(screen.queryByText("• Modified")).not.toBeInTheDocument());
      expect(screen.getByRole("button", { name: "Preview", pressed: true })).toBeInTheDocument();
      expect(screen.getByRole("heading", { name: "Draft to save" })).toBeInTheDocument();
      expect(fileSystemMocks.getFileContent).toHaveBeenCalledTimes(1);
    });

    it("reverts unsaved changes while remaining in preview", async () => {
      render(<FileViewerTab file={mockMarkdown()} isActive />);
      fireEvent.change(await screen.findByTestId("monaco-editor"), { target: { value: "# Draft" } });
      fireEvent.click(screen.getByRole("button", { name: "Preview" }));
      expect(await screen.findByRole("heading", { name: "Draft" })).toBeInTheDocument();
      fireEvent.click(screen.getByRole("button", { name: "Undo" }));
      expect(await screen.findByRole("heading", { name: "Saved heading" })).toBeInTheDocument();
      expect(screen.queryByText("• Modified")).not.toBeInTheDocument();
      expect(screen.getByTestId("monaco-editor")).toHaveValue("# Saved heading");
      expect(screen.getByRole("button", { name: "Preview", pressed: true })).toBeInTheDocument();
      expect(fileSystemMocks.saveFileContent).not.toHaveBeenCalled();
    });

    it("keeps view mode independent between two viewers of the same file", async () => {
      const file = mockMarkdown();
      render(<>
        <section aria-label="First viewer"><FileViewerTab file={file} viewerId="first" /></section>
        <section aria-label="Second viewer"><FileViewerTab file={file} viewerId="second" /></section>
      </>);
      const first = within(screen.getByRole("region", { name: "First viewer" }));
      const second = within(screen.getByRole("region", { name: "Second viewer" }));
      await first.findByTestId("monaco-editor");
      await second.findByTestId("monaco-editor");
      fireEvent.click(first.getByRole("button", { name: "Preview" }));
      expect(await first.findByRole("heading", { name: "Saved heading" })).toBeInTheDocument();
      expect(first.getByRole("button", { name: "Preview", pressed: true })).toBeInTheDocument();
      expect(second.getByRole("button", { name: "Markdown", pressed: true })).toBeInTheDocument();
      expect(second.queryByRole("heading")).not.toBeInTheDocument();
    });

    it.each(["path", "worktree"])("resets to source when file identity changes by %s", async (identity) => {
      const file = mockMarkdown();
      const { rerender } = render(<FileViewerTab file={file} worktreeId="wt-1" isActive />);
      await screen.findByTestId("monaco-editor");
      fireEvent.click(screen.getByRole("button", { name: "Preview" }));
      expect(await screen.findByRole("heading", { name: "Saved heading" })).toBeInTheDocument();
      fileSystemMocks.getFileContent.mockResolvedValue("# Another file");
      rerender(<FileViewerTab
        file={identity === "path" ? { ...file, path: "/workspace/other/README.md" } : file}
        worktreeId={identity === "worktree" ? "wt-2" : "wt-1"}
        isActive
      />);
      await waitFor(() => expect(screen.getByTestId("monaco-editor")).toHaveValue("# Another file"));
      expect(screen.getByRole("button", { name: "Markdown", pressed: true })).toBeInTheDocument();
      expect(screen.getByRole("button", { name: "Preview", pressed: false })).toBeInTheDocument();
      expect(screen.queryByRole("heading")).not.toBeInTheDocument();
    });
  });

  it("renders Monaco for text previews", async () => {
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
    fileSystemMocks.getFileContent.mockResolvedValue("const answer = 42;");

    render(<FileViewerTab file={createFile()} isActive />);

    const editor = await screen.findByTestId("monaco-editor");
    expect(editor).toHaveTextContent("const answer = 42;");
    expect(editor).toHaveAttribute("data-readonly", "false");
    expect(fileSystemMocks.getFileContent).toHaveBeenCalledTimes(1);
    expect(fileSystemMocks.getFilePreviewBlob).not.toHaveBeenCalled();
  });

  it("renders image previews with simplified chrome and cleans them up", async () => {
    fileSystemMocks.getFilePreviewInfo.mockResolvedValue({
      name: "photo.png",
      path: "/workspace/photo.png",
      size: 512,
      modified: "2024-01-01T00:00:00Z",
      viewerKind: "image",
      mimeType: "image/png",
      isBinary: true,
      isEditable: false,
    });
    fileSystemMocks.getFilePreviewBlob.mockResolvedValue(new Blob(["img"], { type: "image/png" }));

    const { unmount } = render(
      <FileViewerTab file={createFile({ name: "photo.png", path: "/workspace/photo.png" })} isActive />
    );

    const image = await screen.findByAltText("photo.png");
    expect(image).toHaveAttribute("src", "blob:preview-url");
    expect(screen.queryByText("This image is shown inline. Click it to inspect at full size.")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^open$/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^reveal$/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^copy path$/i })).not.toBeInTheDocument();
    expect(screen.getAllByText("photo.png")).toHaveLength(1);
    expect(image.closest("button")).toHaveClass("bg-transparent", "p-0");
    expect(fileSystemMocks.getFileContent).not.toHaveBeenCalled();
    expect(createObjectURLSpy).toHaveBeenCalledTimes(1);

    unmount();
    expect(revokeObjectURLSpy).toHaveBeenCalledWith("blob:preview-url");
  });

  it("renders a minimal binary fallback without preview-area controls", async () => {
    fileSystemMocks.getFilePreviewInfo.mockResolvedValue({
      name: "archive.zip",
      path: "/workspace/archive.zip",
      size: 2048,
      modified: "2024-01-01T00:00:00Z",
      viewerKind: "binary",
      mimeType: "application/zip",
      isBinary: true,
      isEditable: false,
    });

    render(
      <FileViewerTab file={createFile({ name: "archive.zip", path: "/workspace/archive.zip" })} isActive />
    );

    expect(await screen.findByText("archive.zip")).toBeInTheDocument();
    expect(screen.getByText("This file type cannot be previewed inline.")).toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^open$/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^reveal/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^copy path$/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^reload$/i })).not.toBeInTheDocument();
    expect(fileSystemMocks.getFileContent).not.toHaveBeenCalled();
    expect(fileSystemMocks.getFilePreviewBlob).not.toHaveBeenCalled();
  });

  it("renders PDFs in Monaco using raw file content instead of the fallback card", async () => {
    fileSystemMocks.getFilePreviewInfo.mockResolvedValue({
      name: "spec.pdf",
      path: "/workspace/spec.pdf",
      size: 1024,
      modified: "2024-01-01T00:00:00Z",
      viewerKind: "pdf",
      mimeType: "application/pdf",
      isBinary: true,
      isEditable: false,
    });
    fileSystemMocks.getFileContent.mockResolvedValue("%PDF-1.4\n1 0 obj\n<< /Type /Catalog >>");

    render(
      <FileViewerTab file={createFile({ name: "spec.pdf", path: "/workspace/spec.pdf" })} isActive />
    );

    const editor = await screen.findByTestId("monaco-editor");
    expect(editor).toHaveTextContent("%PDF-1.4");
    expect(editor).toHaveAttribute("data-readonly", "true");
    expect(screen.queryByText("This file type cannot be previewed inline.")).not.toBeInTheDocument();
    expect(screen.queryByText("Binary File")).not.toBeInTheDocument();
    expect(screen.queryByTitle("PDF preview for spec.pdf")).not.toBeInTheDocument();
    expect(fileSystemMocks.getFileContent).toHaveBeenCalledWith("/workspace/spec.pdf", "wt-1");
    expect(fileSystemMocks.getFilePreviewBlob).not.toHaveBeenCalled();
    expect(createObjectURLSpy).not.toHaveBeenCalled();
  });

  it("hides file viewer chrome when embedded for raw PDF content", async () => {
    fileSystemMocks.getFilePreviewInfo.mockResolvedValue({
      name: "spec.pdf",
      path: "/workspace/spec.pdf",
      size: 1024,
      modified: "2024-01-01T00:00:00Z",
      viewerKind: "pdf",
      mimeType: "application/pdf",
      isBinary: true,
      isEditable: false,
    });
    fileSystemMocks.getFileContent.mockResolvedValue("%PDF-1.4\nembedded");

    render(
      <FileViewerTab
        file={createFile({ name: "spec.pdf", path: "/workspace/spec.pdf" })}
        isActive
        embedded
      />
    );

    const editor = await screen.findByTestId("monaco-editor");
    expect(editor).toHaveTextContent("%PDF-1.4");
    expect(editor).toHaveAttribute("data-readonly", "true");
    expect(screen.queryByText("This file type cannot be previewed inline.")).not.toBeInTheDocument();
    expect(screen.queryByText("workspace/spec.pdf")).not.toBeInTheDocument();
    expect(screen.queryByText("Binary File")).not.toBeInTheDocument();
    expect(screen.queryByText(/Modified:/i)).not.toBeInTheDocument();
    expect(fileSystemMocks.getFilePreviewBlob).not.toHaveBeenCalled();
  });

  it("renders audio previews with native controls and simplified chrome", async () => {
    fileSystemMocks.getFilePreviewInfo.mockResolvedValue({
      name: "sample.mp3",
      path: "/workspace/sample.mp3",
      size: 4096,
      modified: "2024-01-01T00:00:00Z",
      viewerKind: "audio",
      mimeType: "audio/mpeg",
      isBinary: true,
      isEditable: false,
    });
    fileSystemMocks.getFilePreviewBlob.mockResolvedValue(new Blob(["audio"], { type: "audio/mpeg" }));

    const { container } = render(
      <FileViewerTab file={createFile({ name: "sample.mp3", path: "/workspace/sample.mp3" })} isActive />
    );

    await waitFor(() => {
      const audio = container.querySelector("audio");
      expect(audio).not.toBeNull();
      expect(audio).toHaveAttribute("src", "blob:preview-url");
    });

    expect(screen.queryByText("This audio file is available with native playback controls.")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^open$/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^reveal$/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^copy path$/i })).not.toBeInTheDocument();
    expect(fileSystemMocks.getFileContent).not.toHaveBeenCalled();
  });

  it("renders video previews with native controls and simplified chrome", async () => {
    fileSystemMocks.getFilePreviewInfo.mockResolvedValue({
      name: "clip.webm",
      path: "/workspace/clip.webm",
      size: 8192,
      modified: "2024-01-01T00:00:00Z",
      viewerKind: "video",
      mimeType: "video/webm",
      isBinary: true,
      isEditable: false,
    });
    fileSystemMocks.getFilePreviewBlob.mockResolvedValue(new Blob(["video"], { type: "video/webm" }));

    const { container } = render(
      <FileViewerTab file={createFile({ name: "clip.webm", path: "/workspace/clip.webm" })} isActive />
    );

    await waitFor(() => {
      const video = container.querySelector("video");
      expect(video).not.toBeNull();
      expect(video).toHaveAttribute("src", "blob:preview-url");
    });

    expect(screen.queryByText("This video file is available with native playback controls.")).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^open$/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^reveal$/i })).not.toBeInTheDocument();
    expect(screen.queryByRole("button", { name: /^copy path$/i })).not.toBeInTheDocument();
    expect(fileSystemMocks.getFileContent).not.toHaveBeenCalled();
  });
});
