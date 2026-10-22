/**
 * Terminal sessions whose workspace is gone must go too, and a daemon session
 * id must not outlive the connection it was minted for.
 *
 * Prod, 2026-10-08: a terminal for a removed worktree was persisted in
 * localStorage, rendered hidden (every project session mounts a socket), and
 * reconnected to its vanished directory every ~12s for five hours.
 */
import { beforeEach, describe, expect, it, vi } from "vitest";

vi.mock("../../api/terminal-grpc", () => ({
  terminalApi: { closeSession: vi.fn(() => Promise.resolve({ success: true, message: "" })) },
}));
vi.mock("../../lib/logger", () => ({
  logger: { info: vi.fn(), warn: vi.fn(), debug: vi.fn(), error: vi.fn() },
}));
vi.mock("../workspaceStateStore", () => ({
  useWorkspaceStateStore: { getState: () => ({}) },
}));

import { useTerminalStore } from "../terminalStore";

beforeEach(() => {
  useTerminalStore.setState({ sessions: [], activeSessionId: null, activeSessionPerWorktree: {} });
  localStorage.clear();
});

describe("pruneSessions", () => {
  it("drops the sessions of a project's removed workspaces and keeps the rest", () => {
    const store = useTerminalStore.getState();
    const main = store.createSession("/p", "p1");
    const live = store.createSession("/w/live", "p1", "wt-live");
    const gone = store.createSession("/w/gone", "p1", "wt-gone");
    const otherProject = store.createSession("/q", "p2", "wt-other");
    store.setActiveSession(gone);

    useTerminalStore.getState().pruneSessions("p1", new Set(["wt-live"]));

    const ids = useTerminalStore.getState().sessions.map((s) => s.id);
    expect(ids).toEqual([main, live, otherProject]);
    expect(useTerminalStore.getState().activeSessionId).toBeNull();
    expect(Object.values(useTerminalStore.getState().activeSessionPerWorktree)).not.toContain(gone);
  });
});

describe("persistence", () => {
  it("never writes the connection-scoped daemon session id", () => {
    const store = useTerminalStore.getState();
    const id = store.createSession("/p", "p1");
    store.setDaemonSessionId(id, "83328599-641e-4a39-aec5-9a5822ff72fa");

    const stored = localStorage.getItem("terminal-store") ?? "";
    expect(stored).toContain(id);
    expect(stored).not.toContain("83328599");
  });
});
