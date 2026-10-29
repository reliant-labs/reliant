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
const startChat = vi.hoisted(() => vi.fn());
const openChatInWorkspace = vi.hoisted(() => vi.fn());
const trackEvent = vi.hoisted(() => vi.fn());
vi.mock("@/store/chatStore", () => ({ useChatStore: { getState: () => ({ startChat }) } }));
vi.mock("@/lib/openChatInWorkspace", () => ({ openChatInWorkspace }));
vi.mock("@/lib/analytics", () => ({ trackEvent }));

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
    startChat.mockReset().mockImplementation(async (worktreeId: string) => {
      calls.push("start");
      return { id: "chat-1", projectId: "p1", worktreeId };
    });
    openChatInWorkspace.mockReset().mockImplementation(async () => void calls.push("open-chat"));
    trackEvent.mockReset();
  });

  it("sends the prompt as the first message on the new workspace, then opens that chat, when asked to send", async () => {
    const openProject = vi.fn(() => void calls.push("open"));
    const onChatCreated = vi.fn(() => void calls.push("created"));
    const started = await startAskReliantChat({ project: shop, prompt: "fix it", workspaceName: "fix-issue-iss-1-ab12", send: true, onChatCreated, openProject });

    expect(started).toEqual({ worktreeId: "w9", chatId: "chat-1" });
    expect(calls).toEqual(["create", "remember", "start", "created", "open-chat", "open"]);
    expect(startChat).toHaveBeenCalledWith("w9", "fix it");
    expect(onChatCreated).toHaveBeenCalledWith("chat-1");
    expect(openChatInWorkspace).toHaveBeenCalledWith(expect.objectContaining({ id: "chat-1" }), { navigate: true });
    expect(trackEvent).toHaveBeenCalledWith("chat_created", expect.any(Object));
    // Sent: nothing is left waiting in the composer, and the new-chat screen is not opened over the chat.
    expect(useComposerHandoffStore.getState().pending).toBeNull();
    expect(switchWorktreeContext).not.toHaveBeenCalled();
  });

  it("leaves the prompt in the composer and says why when the send fails, so nothing is lost", async () => {
    startChat.mockRejectedValueOnce(new Error("machine asleep"));
    const openProject = vi.fn();
    const onChatCreated = vi.fn();
    const started = await startAskReliantChat({ project: shop, prompt: "fix it", workspaceName: "fix-x", send: true, onChatCreated, openProject });

    expect(started.chatId).toBeUndefined();
    expect(started.sendError).toEqual(new Error("machine asleep"));
    expect(onChatCreated).not.toHaveBeenCalled();
    expect(switchWorktreeContext).toHaveBeenCalledWith("p1", { id: "w9" }, { openFreshNewChat: true });
    expect(useComposerHandoffStore.getState().pending).toMatchObject({ worktreeId: "w9", text: "fix it", onChatCreated });
    expect(openProject).toHaveBeenCalledWith("p1");
  });

  it("does not offer the prompt again when the chat started but could not be opened", async () => {
    openChatInWorkspace.mockRejectedValueOnce(new Error("gone"));
    const openProject = vi.fn();
    const started = await startAskReliantChat({ project: shop, prompt: "fix it", workspaceName: "fix-x", send: true, openProject });

    expect(started.chatId).toBe("chat-1");
    expect(useComposerHandoffStore.getState().pending).toBeNull();
    expect(openProject).toHaveBeenCalledWith("p1");
  });

  it("creates the workspace, switches to it, leaves the prompt in the composer unsent, then opens the project", async () => {
    const openProject = vi.fn(() => void calls.push("open"));
    const onChatCreated = vi.fn();
    const started = await startAskReliantChat({ project: shop, prompt: "the prompt", workspaceName: "ask-issue-iss-1-ab12", onChatCreated, openProject });

    expect(started).toEqual({ worktreeId: "w9" });
    expect(startChat).not.toHaveBeenCalled();
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
