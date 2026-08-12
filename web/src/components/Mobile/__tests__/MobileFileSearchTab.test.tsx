/**
 * MobileFileSearchTab — file content search, tapping through to a read-only
 * Prism preview (`MobileFilePreview`), never the Monaco `FileViewerTab`.
 */

import { render, screen, waitFor } from "@testing-library/react";
import { describe, expect, it, vi, beforeEach } from "vitest";
import userEvent from "@testing-library/user-event";

const searchFiles = vi.fn();
vi.mock("../../../api/fileSystem", () => ({
  searchFiles: (...args: unknown[]) => searchFiles(...args),
}));

// Stable reference across renders — a fresh object literal per call would
// change identity every render and, since it feeds a useEffect dependency
// array, spin the component into an infinite render loop.
const currentProject = { id: "proj-1" };
vi.mock("../../../store/projectStore", () => ({
  useProjectStore: (selector: (s: unknown) => unknown) =>
    selector({ currentProject }),
}));

vi.mock("../../../store/worktreeStore", () => ({
  useActiveWorktreeId: () => "wt-1",
}));

vi.mock("../MobileFilePreview", () => ({
  MobileFilePreview: ({ path }: { path: string }) => (
    <div data-testid="file-preview">preview:{path}</div>
  ),
}));

const { MobileFileSearchTab } = await import("../MobileFileSearchTab");

beforeEach(() => {
  searchFiles.mockReset();
});

describe("MobileFileSearchTab", () => {
  it("shows a prompt before any query", () => {
    render(<MobileFileSearchTab query="" />);
    expect(screen.getByText(/type to search file contents/i)).toBeInTheDocument();
  });

  it("calls searchFiles with the active worktree once a query is typed", async () => {
    searchFiles.mockResolvedValue({ results: [], totalMatches: 0, truncated: false });
    render(<MobileFileSearchTab query="TODO" />);
    await waitFor(() =>
      expect(searchFiles).toHaveBeenCalledWith(
        "TODO",
        expect.objectContaining({ worktreeId: "wt-1" }),
      ),
    );
  });

  it("flattens matches into a single scrollable list", async () => {
    searchFiles.mockResolvedValue({
      results: [
        {
          path: "src/foo.ts",
          matches: [
            {
              lineNumber: 3,
              lineContent: "const TODO = 1;",
              matchStart: 6,
              matchEnd: 10,
              contextBefore: [],
              contextAfter: [],
            },
          ],
        },
      ],
      totalMatches: 1,
      truncated: false,
    });
    render(<MobileFileSearchTab query="TODO" />);
    expect(await screen.findByText("src/foo.ts")).toBeInTheDocument();
    expect(screen.getByText(":3")).toBeInTheDocument();
  });

  it("opens a read-only preview on tap, not the Monaco viewer", async () => {
    searchFiles.mockResolvedValue({
      results: [
        {
          path: "src/foo.ts",
          matches: [
            {
              lineNumber: 3,
              lineContent: "const TODO = 1;",
              matchStart: 6,
              matchEnd: 10,
              contextBefore: [],
              contextAfter: [],
            },
          ],
        },
      ],
      totalMatches: 1,
      truncated: false,
    });
    render(<MobileFileSearchTab query="TODO" />);
    const row = await screen.findByText("src/foo.ts");
    await userEvent.click(row.closest("button")!);
    expect(screen.getByTestId("file-preview")).toHaveTextContent("preview:src/foo.ts");
  });

  it("toggles the regex/case-sensitive/glob options panel", async () => {
    searchFiles.mockResolvedValue({ results: [], totalMatches: 0, truncated: false });
    render(<MobileFileSearchTab query="" />);
    await userEvent.click(screen.getByLabelText(/search options/i));
    expect(screen.getByText(/case sensitive/i)).toBeInTheDocument();
    expect(screen.getByPlaceholderText(/\*\.ts, \*\.go/)).toBeInTheDocument();
  });
});
