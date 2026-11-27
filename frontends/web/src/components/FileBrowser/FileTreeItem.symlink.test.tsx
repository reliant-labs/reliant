import { fireEvent, render, screen } from "@testing-library/react";
import { FileTreeItem } from "./FileTreeItem";
import type { FileNode } from "./index";

// The row itself: a symlinked folder draws a chevron and toggles like any
// folder; a link the daemon refused to follow draws none, does nothing on
// click, and says why.

vi.mock("../../store/projectStore", () => ({
  useProjectStore: (selector: (s: { currentProject: { id: string } }) => unknown) => selector({ currentProject: { id: "proj-1" } }),
}));
vi.mock("../../store/viewerStore", () => ({ useViewerStore: () => ({}) }));
vi.mock("../../store/fileClipboardStore", () => ({
  useFileClipboardStore: () => ({ setClipboard: vi.fn(), clearClipboard: vi.fn() }),
}));
vi.mock("../../hooks/worktree-queries", () => ({
  useCurrentWorktree: () => ({ id: "wt-1", path: "/workspace" }),
  useWorktreeById: () => undefined,
}));
vi.mock("../../hooks/settings-queries", () => ({ cachedUserPreferences: () => ({}) }));
vi.mock("../../lib/toast-manager", () => ({ toast: { notify: vi.fn() } }));

function renderRow(node: FileNode, onSelect = vi.fn()) {
  render(
    <FileTreeItem
      node={node}
      level={0}
      onSelect={onSelect}
      onPathChange={vi.fn()}
      selectedFile={null}
      onRefresh={vi.fn()}
      expandedPaths={new Set()}
      onExpand={vi.fn()}
      onCollapse={vi.fn()}
    />
  );
  const row = document.querySelector(`[data-path="${node.path}"]`) as HTMLElement;
  return { row, onSelect };
}

const hasChevron = (row: HTMLElement) => row.querySelector(".lucide-chevron-right, .lucide-chevron-down") !== null;

describe("FileTreeItem symbolic links", () => {
  it("draws a chevron for a symlinked folder and selects it on click", () => {
    const { row, onSelect } = renderRow({
      name: "shared",
      path: "shared",
      type: "directory",
      hasChildren: true,
      symlinkTarget: "../shared",
      symlinkStatus: "resolved",
    });
    expect(hasChevron(row)).toBe(true);
    expect(screen.getByLabelText("symbolic link")).toBeInTheDocument();
    fireEvent.click(row);
    expect(onSelect).toHaveBeenCalledTimes(1);
  });

  it("draws no chevron for a looping link, ignores clicks, and names the reason", () => {
    const { row, onSelect } = renderRow({
      name: "up",
      path: "up",
      type: "directory",
      hasChildren: true,
      symlinkTarget: "..",
      symlinkStatus: "loop",
    });
    expect(hasChevron(row)).toBe(false);
    expect(screen.getByLabelText("Symbolic link → .. to a folder that contains it")).toBeInTheDocument();
    fireEvent.click(row);
    expect(onSelect).not.toHaveBeenCalled();
  });

  it("does not open a dangling link", () => {
    const { row, onSelect } = renderRow({
      name: "gone",
      path: "gone",
      type: "file",
      symlinkTarget: "missing",
      symlinkStatus: "broken",
    });
    expect(screen.getByText("gone")).toHaveAttribute("data-unavailable", "true");
    fireEvent.click(row);
    expect(onSelect).not.toHaveBeenCalled();
  });

  it("still draws a chevron for a plain folder with children", () => {
    const { row } = renderRow({ name: ".reliant.local", path: ".reliant.local", type: "directory", hasChildren: true });
    expect(hasChevron(row)).toBe(true);
  });
});
