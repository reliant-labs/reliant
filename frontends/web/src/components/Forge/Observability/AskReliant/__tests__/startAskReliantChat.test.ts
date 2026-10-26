// Copyright (c) 2025 Reliant Labs

import { beforeEach, describe, expect, it, vi } from "vitest";

import { WorktreeStatus } from "@/gen/reliant/v1/worktree_pb";
import { useComposerHandoffStore } from "@/store/composerHandoffStore";
import type { Project } from "@/store/projectStore";

const calls: string[] = [];
const createWorktree = vi.hoisted(() => vi.fn());
const switchWorktreeContext = vi.hoisted(() => vi.fn());
const selectProject = vi.hoisted(() => vi.fn());
const setLastWorktree = vi.hoisted(() => vi.fn());
const projectState = vi.hoisted(() => ({ currentProject: null as { id: string } | null }));

vi.mock("@/hooks/worktree-actions", () => ({ createWorktree }));
vi.mock("@/store/worktreeStore", () => ({ useWorktreeStore: { getState: () => ({ switchWorktreeContext }) } }));
vi.mock("@/store/projectStore", () => ({ useProjectStore: { getState: () => ({ currentProject: projectState.currentProject, selectProject }) } }));
vi.mock("@/store/workspaceStateStore", () => ({ useWorkspaceStateStore: { getState: () => ({ setLastWorktree }) } }));

import { startAskReliantChat } from "../startAskReliantChat";

const shop = { id: "p1", name: "Shop" } as Project;

describe("startAskReliantChat", () => {
  beforeEach(() => {
    calls.length = 0;
    useComposerHandoffStore.setState({ pending: null });
    projectState.currentProject = { id: "p1" };
    createWorktree.mockReset().mockImplementation(async () => {
      calls.push("create");
      return { id: "w9" };
    });
    switchWorktreeContext.mockReset().mockImplementation(async () => void calls.push("switch"));
    selectProject.mockReset().mockImplementation(async () => void calls.push("select"));
    setLastWorktree.mockReset().mockImplementation(() => void calls.push("remember"));
  });

  it("creates the workspace, switches to it, leaves the prompt in the composer unsent, then opens the project", async () => {
    const openProject = vi.fn(() => void calls.push("open"));
    const onChatCreated = vi.fn();
    const { worktreeId } = await startAskReliantChat({ project: shop, prompt: "the prompt", workspaceName: "ask-issue-iss-1-ab12", onChatCreated, openProject });

    expect(worktreeId).toBe("w9");
    expect(calls).toEqual(["create", "remember", "switch", "open"]);
    expect(createWorktree).toHaveBeenCalledWith(
      expect.objectContaining({ name: "ask-issue-iss-1-ab12", branch: "ask-issue-iss-1-ab12", project_id: "p1", status: WorktreeStatus.ACTIVE })
    );
    expect(switchWorktreeContext).toHaveBeenCalledWith("p1", { id: "w9" }, { openFreshNewChat: true });
    expect(setLastWorktree).toHaveBeenCalledWith("p1", "w9");
    expect(openProject).toHaveBeenCalledWith("p1");

    const offered = useComposerHandoffStore.getState().pending;
    expect(offered).toMatchObject({ projectId: "p1", worktreeId: "w9", text: "the prompt", onChatCreated });
  });

  it("selects the project first when another is open", async () => {
    projectState.currentProject = { id: "other" };
    await startAskReliantChat({ project: shop, prompt: "p", workspaceName: "ask-x", openProject: vi.fn() });
    expect(calls[0]).toBe("select");
    expect(selectProject).toHaveBeenCalledWith(shop);
  });

  it("offers nothing and does not navigate when the workspace cannot be created", async () => {
    createWorktree.mockRejectedValueOnce(new Error("no git"));
    const openProject = vi.fn();
    await expect(startAskReliantChat({ project: shop, prompt: "p", workspaceName: "ask-x", openProject })).rejects.toThrow("no git");
    expect(useComposerHandoffStore.getState().pending).toBeNull();
    expect(openProject).not.toHaveBeenCalled();
  });
});
