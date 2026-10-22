/**
 * Closing a terminal must address the DAEMON's session id, not the local one.
 *
 * The store mints `terminal-<ts>-<rand>` when a tab is created, because React
 * needs a key before any socket exists. The daemon mints its own UUID when it
 * actually forks the PTY. Those are different namespaces, and only the second
 * one can close anything.
 *
 * Sending the local id — which is what this code used to do — produced
 * `CloseSession ... session not found: terminal-1789117900104-d1qzvmsih` in
 * prod on every single terminal close. The failure was swallowed as a
 * `logger.warn`, so the UI looked correct while the shell process kept running
 * inside the user's workspace until the pod died. That is the leak these tests
 * exist to prevent, and it is invisible from the frontend alone — which is why
 * the assertion is on the ARGUMENT, not on the call count.
 */
import { describe, expect, it, vi, beforeEach } from "vitest";

const closeSession = vi.fn(() => Promise.resolve({ success: true, message: "" }));

vi.mock("../../api/terminal-grpc", () => ({
  terminalApi: {
    closeSession: (id: string) => closeSession(id),
  },
}));

vi.mock("../../lib/logger", () => ({
  logger: { info: vi.fn(), warn: vi.fn(), debug: vi.fn(), error: vi.fn() },
}));

vi.mock("../workspaceStateStore", () => ({
  useWorkspaceStateStore: { getState: () => ({}) },
}));

import { useTerminalStore } from "../terminalStore";

function reset() {
  useTerminalStore.setState({
    sessions: [],
    activeSessionId: null,
    activeSessionPerWorktree: {},
  });
  closeSession.mockClear();
}

describe("terminal close uses the daemon session id", () => {
  beforeEach(reset);

  it("sends the daemon id, never the local id", () => {
    const localId = useTerminalStore.getState().createSession("/home/workspace");
    const daemonId = "83328599-641e-4a39-aec5-9a5822ff72fa";

    useTerminalStore.getState().setDaemonSessionId(localId, daemonId);
    useTerminalStore.getState().killSession(localId);

    expect(closeSession).toHaveBeenCalledWith(daemonId);
    // The precise regression: the local id must never reach the wire.
    expect(closeSession).not.toHaveBeenCalledWith(localId);
  });

  it("does not call the server when no daemon session was ever created", () => {
    // "init" never arrived — the socket failed, or the daemon was offline. No
    // PTY exists, so there is nothing to close. Calling CloseSession here with
    // the local id is exactly what generated the prod error noise.
    const localId = useTerminalStore.getState().createSession("/home/workspace");

    useTerminalStore.getState().killSession(localId);

    expect(closeSession).not.toHaveBeenCalled();
  });

  it("still removes the tab locally when the server close fails", () => {
    // The close is fire-and-forget on purpose: a session can legitimately be
    // gone already (user typed `exit`, daemon restarted). A rejected close must
    // not strand the tab in the UI.
    closeSession.mockImplementationOnce(() => Promise.reject(new Error("boom")));

    const localId = useTerminalStore.getState().createSession("/home/workspace");
    useTerminalStore.getState().setDaemonSessionId(localId, "some-uuid");
    useTerminalStore.getState().killSession(localId);

    expect(
      useTerminalStore.getState().sessions.find((s) => s.id === localId),
    ).toBeUndefined();
  });

  it("keeps the local id as the store key", () => {
    // The daemon id is for addressing the PTY; the local id remains the
    // identity the React tree and activeSessionId use. Conflating them would
    // break every consumer that holds a session id across the socket lifecycle.
    const localId = useTerminalStore.getState().createSession("/home/workspace");
    useTerminalStore.getState().setDaemonSessionId(localId, "a-uuid");

    const session = useTerminalStore.getState().sessions.find((s) => s.id === localId);
    expect(session?.id).toBe(localId);
    expect(session?.daemonSessionId).toBe("a-uuid");
    expect(useTerminalStore.getState().activeSessionId).toBe(localId);
  });
});
