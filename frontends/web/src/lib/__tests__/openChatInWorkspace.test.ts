import { beforeEach, describe, expect, it, vi } from "vitest";
import type { Chat } from "../../api/client";

const calls: string[] = [];
const worktreeGate: { release: () => void; promise: Promise<unknown> } = {
  release: () => {},
  promise: Promise.resolve(),
};

const state = vi.hoisted(() => ({
  activeChatId: null as string | null,
  currentProjectId: "project-1" as string | undefined,
  worktrees: new Map<string, { id: string }>(),
}));

vi.mock("../../store/chatStore", () => ({
  useChatStore: {
    getState: () => ({
      get activeChatId() {
        return state.activeChatId;
      },
      selectChat: (chat: Chat, options?: { deferPersist?: boolean }) => {
        calls.push(`selectChat:${chat.id}:${options?.deferPersist ? "deferred" : "persisted"}`);
        state.activeChatId = chat.id;
      },
      persistActiveChat: (chatId: string) => calls.push(`persistActiveChat:${chatId}`),
    }),
  },
}));

vi.mock("../../store/chatNavigationStore", () => ({
  useChatNavigationStore: {
    getState: () => ({
      navigateToChat: (chatId: string) => calls.push(`navigateToChat:${chatId}`),
    }),
  },
}));

vi.mock("../../store/projectStore", () => ({
  useProjectStore: {
    getState: () => ({ currentProject: state.currentProjectId ? { id: state.currentProjectId } : null }),
  },
}));

vi.mock("../../hooks/worktree-queries", () => ({
  resolveWorktree: async (id: string) => {
    calls.push(`resolveWorktree:${id}:start`);
    await worktreeGate.promise;
    calls.push(`resolveWorktree:${id}:end`);
    return state.worktrees.get(id) ?? null;
  },
}));

vi.mock("../../store/worktreeStore", () => ({
  useWorktreeStore: {
    getState: () => ({
      switchWorktreeContext: async (projectId: string, worktree: { id: string } | null) => {
        calls.push(`switchWorktreeContext:${projectId}:${worktree?.id ?? "main"}`);
      },
    }),
  },
}));

vi.mock("../../hooks/project-queries", () => ({
  getCachedProjects: () => [],
  fetchProjects: async () => [],
}));

import { openChatInWorkspace } from "../openChatInWorkspace";

function chat(id: string, worktreeId?: string): Chat {
  return { id, worktreeId } as Chat;
}

function gate() {
  let release!: () => void;
  const promise = new Promise<void>((resolve) => {
    release = resolve;
  });
  worktreeGate.release = release;
  worktreeGate.promise = promise;
}

beforeEach(() => {
  calls.length = 0;
  state.activeChatId = null;
  state.currentProjectId = "project-1";
  state.worktrees = new Map([["wt-1", { id: "wt-1" }]]);
  worktreeGate.release = () => {};
  worktreeGate.promise = Promise.resolve();
});

describe("openChatInWorkspace", () => {
  it("selects the chat before the worktree has resolved", async () => {
    gate();
    const opened = openChatInWorkspace(chat("c1", "wt-1"), { navigate: true });

    // Nothing has been released: the worktree lookup is still outstanding,
    // and the chat is already selected (the stream subscribe starts there).
    await Promise.resolve();
    expect(calls).toEqual(["selectChat:c1:deferred", "resolveWorktree:wt-1:start"]);

    worktreeGate.release();
    await opened;
    expect(calls).toEqual([
      "selectChat:c1:deferred",
      "resolveWorktree:wt-1:start",
      "resolveWorktree:wt-1:end",
      "switchWorktreeContext:project-1:wt-1",
      "persistActiveChat:c1",
      "navigateToChat:c1",
    ]);
  });

  it("records the active chat and queue only once the worktree context is current", async () => {
    await openChatInWorkspace(chat("c1", "wt-1"), { navigate: true });
    const switchAt = calls.indexOf("switchWorktreeContext:project-1:wt-1");
    expect(switchAt).toBeGreaterThan(-1);
    expect(calls.indexOf("persistActiveChat:c1")).toBeGreaterThan(switchAt);
    expect(calls.indexOf("navigateToChat:c1")).toBeGreaterThan(switchAt);
  });

  it("switches to the main workspace for a chat with no worktree", async () => {
    await openChatInWorkspace(chat("c1"));
    expect(calls).toEqual([
      "selectChat:c1:deferred",
      "switchWorktreeContext:project-1:main",
      "persistActiveChat:c1",
    ]);
  });

  it("does not navigate unless asked to", async () => {
    await openChatInWorkspace(chat("c1", "wt-1"));
    expect(calls.some((c) => c.startsWith("navigateToChat"))).toBe(false);
  });

  it("still opens a chat whose worktree cannot be resolved", async () => {
    state.worktrees.clear();
    await openChatInWorkspace(chat("c1", "wt-gone"), { navigate: true });
    expect(calls).toEqual([
      "selectChat:c1:deferred",
      "resolveWorktree:wt-gone:start",
      "resolveWorktree:wt-gone:end",
      "persistActiveChat:c1",
      "navigateToChat:c1",
    ]);
  });

  it("leaves the context alone when another chat was opened meanwhile", async () => {
    gate();
    const first = openChatInWorkspace(chat("c1", "wt-1"), { navigate: true });
    await Promise.resolve();

    // The user clicks a different chat while c1's worktree is still resolving.
    state.activeChatId = "c2";
    worktreeGate.release();
    await first;

    expect(calls).not.toContain("switchWorktreeContext:project-1:wt-1");
    expect(calls).not.toContain("persistActiveChat:c1");
    expect(calls).not.toContain("navigateToChat:c1");
  });

  it("does not switch or persist when the project changes during resolution", async () => {
    gate();
    const opened = openChatInWorkspace(chat("c1", "wt-1"), { navigate: true });
    state.currentProjectId = "project-2";
    worktreeGate.release();
    await opened;

    expect(state.activeChatId).toBe("c1");
    expect(calls).not.toContain("switchWorktreeContext:project-1:wt-1");
    expect(calls).not.toContain("persistActiveChat:c1");
    expect(calls).not.toContain("navigateToChat:c1");
  });

  it("only persists the latest open when the same chat is reopened during resolution", async () => {
    gate();
    const old = openChatInWorkspace(chat("c1", "wt-1"), { navigate: true });
    const fresh = openChatInWorkspace(chat("c1", "wt-1"));
    worktreeGate.release();
    await Promise.all([old, fresh]);

    expect(calls.filter((call) => call === "switchWorktreeContext:project-1:wt-1")).toHaveLength(1);
    expect(calls.filter((call) => call === "persistActiveChat:c1")).toHaveLength(1);
    expect(calls).not.toContain("navigateToChat:c1");
  });

  it("selects the chat even with no project", async () => {
    state.currentProjectId = undefined;
    await openChatInWorkspace(chat("c1", "wt-1"), { navigate: true });
    expect(calls).toEqual([
      "selectChat:c1:deferred",
      "persistActiveChat:c1",
      "navigateToChat:c1",
    ]);
  });
});
