/**
 * The Projects viewer tab (ProjectPanel) always offers "Add project", and it
 * opens the Add project dialog — over the window, never a page that replaces
 * it. (Settings → Projects has its own page, ProjectsSection; see
 * ProjectSettings.section.test.tsx.)
 */
import { screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { setTransport } from "@/lib/connect";
import { mockTransport } from "@/lib/test-utils";
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
// Workspace rows are read through hooks/worktree-queries; answer them from the
// same fixture.
vi.mock("@/hooks/worktree-queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/worktree-queries")>()),
  ...(await import("@/test/worktreeQueriesMock")).worktreeQueriesMock(() => ({
      worktrees: [],
      loadWorktrees: vi.fn(),
      currentWorktree: null,
      switchWorktreeContext: vi.fn(),
    })),
}));

vi.mock("@/store/chatStore", () => ({
  useChatStore: (selector: (s: unknown) => unknown) => selector({ selectChat: vi.fn() }),
}));

vi.mock("@/store/chatNavigationStore", () => ({
  useChatNavigationStore: (selector: (s: unknown) => unknown) =>
    selector({ navigateToChat: vi.fn() }),
}));

beforeEach(() => {
  setTransport(mockTransport({ "ProjectService.GetProjectGitInfo": { response: {} } }));
});

afterEach(() => {
  setTransport(null);
});

import { ProjectPanel } from "@/components/Projects/ProjectPanel";
import { useAddProjectDialogStore } from "@/store/addProjectDialogStore";

describe("Projects viewer tab add-project entry point", () => {
  beforeEach(() => {
    useAddProjectDialogStore.setState({ isOpen: false, method: null });
  });

  it("opens the Add project dialog", async () => {
    renderWithQuery(<ProjectPanel />);

    const button = await screen.findByTestId("project-panel-add-project");
    expect(button).toHaveTextContent(/add project/i);

    await userEvent.click(button);
    expect(useAddProjectDialogStore.getState().isOpen).toBe(true);
    expect(useAddProjectDialogStore.getState().method).toBeNull();
  });
});
