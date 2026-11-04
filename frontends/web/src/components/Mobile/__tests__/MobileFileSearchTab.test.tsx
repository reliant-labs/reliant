/**
 * MobileFileSearchTab — file content search, tapping through to a read-only
 * Prism preview (`MobileFilePreview`), never the Monaco `FileViewerTab`.
 *
 * The search is the generated SearchFiles read (hooks/file-system-queries),
 * answered here at the transport.
 */

import { screen, waitFor } from "@testing-library/react";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import userEvent from "@testing-library/user-event";

import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
import { useChatStore } from "@/store/chatStore";
import { useProjectStore } from "@/store/projectStore";
import { renderWithQuery as render } from "@/test/renderWithQuery";
import { serviceHandlers } from "@/test/serviceTransport";

vi.mock("../../../hooks/worktree-queries", () => ({
  useActiveWorktreeId: () => "wt-1",
}));

vi.mock("../MobileFilePreview", () => ({
  MobileFilePreview: ({ path }: { path: string }) => (
    <div data-testid="file-preview">preview:{path}</div>
  ),
}));

const { MobileFileSearchTab } = await import("../MobileFileSearchTab");

const searchFiles = vi.fn();

const ONE_MATCH = {
  results: [
    {
      path: "src/foo.ts",
      matches: [{ lineNumber: 3, lineContent: "const TODO = 1;", matchStart: 6, matchEnd: 10 }],
    },
  ],
  totalMatches: 1,
  truncated: false,
};

beforeEach(() => {
  searchFiles.mockReset();
  setTransport(mockTransport(serviceHandlers("FileSystemService", { searchFiles })));
  useProjectStore.setState({ currentProject: { id: "proj-1", name: "p", path: "/p" } } as never);
  useChatStore.setState({ activeChatId: null });
});

afterEach(() => {
  setTransport(null);
  useProjectStore.setState({ currentProject: null } as never);
});

describe("MobileFileSearchTab", () => {
  it("shows a prompt before any query", () => {
    render(<MobileFileSearchTab query="" />);
    expect(screen.getByText(/type to search file contents/i)).toBeInTheDocument();
    expect(searchFiles).not.toHaveBeenCalled();
  });

  it("searches the active worktree once a query is typed", async () => {
    searchFiles.mockResolvedValue({ results: [], totalMatches: 0, truncated: false });
    render(<MobileFileSearchTab query="TODO" />);
    await waitFor(() => expect(searchFiles).toHaveBeenCalledTimes(1));
    expect(searchFiles.mock.calls[0][0]).toMatchObject({
      projectId: "proj-1",
      query: "TODO",
      worktreeId: "wt-1",
      maxResults: 50,
      contextLines: 0,
    });
  });

  it("flattens matches into a single scrollable list", async () => {
    searchFiles.mockResolvedValue(ONE_MATCH);
    render(<MobileFileSearchTab query="TODO" />);
    expect(await screen.findByText("src/foo.ts")).toBeInTheDocument();
    expect(screen.getByText(":3")).toBeInTheDocument();
  });

  it("opens a read-only preview on tap, not the Monaco viewer", async () => {
    searchFiles.mockResolvedValue(ONE_MATCH);
    render(<MobileFileSearchTab query="TODO" />);
    const row = await screen.findByText("src/foo.ts");
    await userEvent.click(row.closest("button")!);
    expect(screen.getByTestId("file-preview")).toHaveTextContent("preview:src/foo.ts");
  });

  it("toggles the regex/case-sensitive/glob options panel", async () => {
    render(<MobileFileSearchTab query="" />);
    await userEvent.click(screen.getByLabelText(/search options/i));
    expect(screen.getByText(/case sensitive/i)).toBeInTheDocument();
    expect(screen.getByPlaceholderText(/\*\.ts, \*\.go/)).toBeInTheDocument();
  });
});
