/**
 * The Projects viewer tab (ProjectPanel) offers "Add project" only when its
 * host wires in picker navigation.
 *
 * ProjectPanel gates its picker affordance on `onNavigateToProjectPicker`
 * being supplied. Settings once rendered it with no props, so the button
 * existed in the component and was unreachable — indistinguishable from it not
 * existing. Settings → Projects now has its own page (ProjectsSection, see
 * ProjectSettings.section.test.tsx); ProjectPanel remains the viewer tab's
 * surface, and TabbedViewerPanel does wire the navigation in.
 */
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

import { renderWithQuery } from "@/test/renderWithQuery";

vi.mock("@/hooks/chat-queries", () => ({
  useChatList: () => ({ data: [] }),
}));

const currentProject = {
  id: "proj-1",
  name: "my-app",
  path: "/home/workspace/projects/my-app",
  is_git_repo: true,
  default_branch: "main",
  worktree_count: 0,
  last_active: "2025-01-01T00:00:00Z",
  created_at: "2025-01-01T00:00:00Z",
  updated_at: "2025-01-01T00:00:00Z",
  is_forge: false,
};

vi.mock("@/store/projectStore", () => {
  const useProjectStore = (selector: (s: unknown) => unknown) =>
    selector({
      currentProject,
      refreshCurrentProject: vi.fn(),
      deleteProject: vi.fn(),
    });
  return { useProjectStore };
});

vi.mock("@/store/worktreeStore", () => ({
  useWorktreeStore: (selector: (s: unknown) => unknown) =>
    selector({
      worktrees: [],
      loadWorktrees: vi.fn(),
      currentWorktree: null,
      switchWorktreeContext: vi.fn(),
    }),
}));

vi.mock("@/store/chatStore", () => ({
  useChatStore: (selector: (s: unknown) => unknown) => selector({ selectChat: vi.fn() }),
}));

vi.mock("@/store/chatNavigationStore", () => ({
  useChatNavigationStore: (selector: (s: unknown) => unknown) =>
    selector({ navigateToChat: vi.fn() }),
}));

vi.mock("@/api/project-grpc", () => ({
  projectGrpc: {
    getGitInfo: vi.fn(async () => null),
    getGitStatus: vi.fn(async () => null),
  },
}));

import { ProjectPanel } from "@/components/Projects/ProjectPanel";

describe("Settings → Projects add-project entry point", () => {
  it("offers Add project when the picker navigation is wired in", async () => {
    const navigate = vi.fn();
    renderWithQuery(<ProjectPanel onNavigateToProjectPicker={navigate} />);

    const button = await screen.findByTestId("project-panel-add-project");
    expect(button).toHaveTextContent(/add project/i);

    await userEvent.click(button);
    expect(navigate).toHaveBeenCalledTimes(1);
  });

  it("has no add-project affordance at all when navigation is not supplied", () => {
    // This is exactly the state Settings shipped in, and the reason the
    // button has to be wired up rather than merely present in the component.
    renderWithQuery(<ProjectPanel />);

    expect(screen.queryByTestId("project-panel-add-project")).toBeNull();
  });
});
