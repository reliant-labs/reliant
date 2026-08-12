/**
 * MobileWorkspaceSheet is the chat-header drill-in for Files/Git/Plan/
 * Packages. This pins tab switching, the `fileViewer` capability gate, and
 * that closing hides the sheet — the panels themselves are mocked out since
 * their own behavior belongs to their own test files.
 */

import { describe, expect, it, vi } from "vitest";
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { SurfaceProvider } from "../../../lib/surfaceContext";

vi.mock("../MobileFilesPanel", () => ({
  MobileFilesPanel: () => <div data-testid="files-panel">files</div>,
}));

vi.mock("../../Git/GitStatus", () => ({
  GitStatus: () => <div data-testid="git-panel">git</div>,
}));

vi.mock("../../Chat/TasksPanel", () => ({
  TasksPanel: () => <div data-testid="plan-panel">plan</div>,
}));

vi.mock("../MobilePackagesPanel", () => ({
  MobilePackagesPanel: () => <div data-testid="packages-panel">packages</div>,
}));

const { MobileWorkspaceSheet } = await import("../MobileWorkspaceSheet");

describe("MobileWorkspaceSheet", () => {
  it("renders nothing when closed", () => {
    render(
      <SurfaceProvider surface="mobile">
        <MobileWorkspaceSheet isOpen={false} onClose={vi.fn()} worktreeId="wt-1" />
      </SurfaceProvider>,
    );
    expect(screen.queryByText("Workspace")).not.toBeInTheDocument();
  });

  it("shows the Files tab by default", () => {
    render(
      <SurfaceProvider surface="mobile">
        <MobileWorkspaceSheet isOpen onClose={vi.fn()} worktreeId="wt-1" />
      </SurfaceProvider>,
    );
    expect(screen.getByTestId("files-panel")).toBeInTheDocument();
  });

  it("switches to the Git tab", async () => {
    const user = userEvent.setup();
    render(
      <SurfaceProvider surface="mobile">
        <MobileWorkspaceSheet isOpen onClose={vi.fn()} worktreeId="wt-1" />
      </SurfaceProvider>,
    );
    await user.click(screen.getByRole("button", { name: "Git" }));
    expect(screen.getByTestId("git-panel")).toBeInTheDocument();
  });

  it("switches to the Plan tab", async () => {
    const user = userEvent.setup();
    render(
      <SurfaceProvider surface="mobile">
        <MobileWorkspaceSheet isOpen onClose={vi.fn()} worktreeId="wt-1" chatId="chat-1" />
      </SurfaceProvider>,
    );
    await user.click(screen.getByRole("button", { name: "Plan" }));
    expect(screen.getByTestId("plan-panel")).toBeInTheDocument();
  });

  it("switches to the Packages tab", async () => {
    const user = userEvent.setup();
    render(
      <SurfaceProvider surface="mobile">
        <MobileWorkspaceSheet isOpen onClose={vi.fn()} worktreeId="wt-1" />
      </SurfaceProvider>,
    );
    await user.click(screen.getByRole("button", { name: "Packages" }));
    expect(screen.getByTestId("packages-panel")).toBeInTheDocument();
  });

  it("calls onClose when the close button is pressed", async () => {
    const user = userEvent.setup();
    const onClose = vi.fn();
    render(
      <SurfaceProvider surface="mobile">
        <MobileWorkspaceSheet isOpen onClose={onClose} worktreeId="wt-1" />
      </SurfaceProvider>,
    );
    await user.click(screen.getByRole("button", { name: "Close workspace" }));
    expect(onClose).toHaveBeenCalled();
  });

  it("hides the Files panel behind the fileViewer capability gate", () => {
    render(
      <SurfaceProvider surface="embed">
        <MobileWorkspaceSheet isOpen onClose={vi.fn()} worktreeId="wt-1" />
      </SurfaceProvider>,
    );
    expect(screen.queryByTestId("files-panel")).not.toBeInTheDocument();
    expect(screen.getByText("Files are unavailable on this surface.")).toBeInTheDocument();
  });

  it("shows an empty state on the Git tab without a worktree", async () => {
    const user = userEvent.setup();
    render(
      <SurfaceProvider surface="mobile">
        <MobileWorkspaceSheet isOpen onClose={vi.fn()} />
      </SurfaceProvider>,
    );
    await user.click(screen.getByRole("button", { name: "Git" }));
    expect(screen.getByText("No workspace selected.")).toBeInTheDocument();
  });
});
