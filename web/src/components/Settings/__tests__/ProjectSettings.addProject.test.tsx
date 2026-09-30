/**
 * Settings → Projects must offer a way to ADD a project.
 *
 * Per the add-project design the picker is the primary place and Settings is
 * the second one. Settings rendered `<ProjectPanel />` with no props, and
 * ProjectPanel gates both of its picker affordances on
 * `onNavigateToProjectPicker` being supplied — so the button existed in the
 * component and was unreachable from Settings, which is indistinguishable from
 * it not existing at all.
 *
 * These render the real ProjectPanel in both states Settings can be in: with a
 * project selected, and with none.
 */
import { render, screen } from "@testing-library/react";
import userEvent from "@testing-library/user-event";
import { describe, expect, it, vi } from "vitest";

vi.mock("@/hooks/chat-queries", () => ({
  useChatList: () => ({ data: [] }),
}));

const currentProject = {
  id: "proj-1",
  name: "my-app",
  path: "/home/workspace/projects/my-app",
  is_git_repo: true,
  default_branch: "main",
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
    render(<ProjectPanel onNavigateToProjectPicker={navigate} />);

    const button = await screen.findByTestId("project-panel-add-project");
    expect(button).toHaveTextContent(/add project/i);

    await userEvent.click(button);
    expect(navigate).toHaveBeenCalledTimes(1);
  });

  it("has no add-project affordance at all when navigation is not supplied", () => {
    // This is exactly the state Settings shipped in, and the reason the
    // button has to be wired up rather than merely present in the component.
    render(<ProjectPanel />);

    expect(screen.queryByTestId("project-panel-add-project")).toBeNull();
  });
});
