import { describe, expect, it, vi } from "vitest";
import { act, render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { MobileFilesPanel } from "../MobileFilesPanel";

let onFileSelectCapture: ((file: { name: string; path: string; type: string }) => void) | null =
  null;

vi.mock("../../FileBrowser/FileTree", () => ({
  FileTree: ({ onFileSelect }: { onFileSelect: (file: { name: string; path: string; type: string }) => void }) => {
    onFileSelectCapture = onFileSelect;
    return <div data-testid="file-tree" />;
  },
}));

vi.mock("../MobileFilePreview", () => ({
  MobileFilePreview: ({ path }: { path: string }) => (
    <div data-testid="file-preview">{path}</div>
  ),
}));

describe("MobileFilesPanel", () => {
  it("shows the file tree by default", () => {
    render(<MobileFilesPanel worktreeId="wt-1" />);
    expect(screen.getByTestId("file-tree")).toBeInTheDocument();
  });

  it("shows a preview after selecting a file, and returns to the tree via back", async () => {
    const user = userEvent.setup();
    render(<MobileFilesPanel worktreeId="wt-1" />);

    act(() => {
      onFileSelectCapture?.({ name: "index.ts", path: "src/index.ts", type: "file" });
    });

    const preview = screen.getByTestId("file-preview");
    expect(preview).toHaveTextContent("src/index.ts");

    await user.click(screen.getByRole("button", { name: "Back to file list" }));
    expect(screen.getByTestId("file-tree")).toBeInTheDocument();
  });

  it("ignores directory selections", () => {
    render(<MobileFilesPanel worktreeId="wt-1" />);
    act(() => {
      onFileSelectCapture?.({ name: "src", path: "src", type: "directory" });
    });
    expect(screen.queryByTestId("file-preview")).not.toBeInTheDocument();
  });
});
