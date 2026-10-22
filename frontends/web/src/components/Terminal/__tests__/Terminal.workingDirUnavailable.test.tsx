import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import React from "react";

// ---------------------------------------------------------------------------
// A terminal can be asked for a directory that is not on the machine yet. The
// prod case: the owner opened a project ~500ms after its clone was queued, so
// /home/workspace/projects/forge did not exist when the terminal connected.
// The daemon used to start the shell in $HOME instead and say nothing; it now
// refuses, and the server reports `code: "working_dir_unavailable"`.
//
// These tests pin what the user sees for that refusal: the terminal says which
// directory it is waiting for, keeps retrying for as long as it takes (a clone
// outlasts the normal reconnect budget), and opens the shell THERE once the
// directory appears — never a red error in the scrollback, never a give-up.
// ---------------------------------------------------------------------------

const sockets: FakeWebSocket[] = [];

class FakeWebSocket {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSING = 2;
  static readonly CLOSED = 3;

  readyState = FakeWebSocket.CONNECTING;
  onopen: (() => void) | null = null;
  onmessage: ((event: { data: string }) => void) | null = null;
  onerror: ((error: unknown) => void) | null = null;
  onclose: ((event: { code: number; reason: string }) => void) | null = null;
  sent: string[] = [];

  constructor(public url: string) {
    sockets.push(this);
  }

  send(data: string) {
    this.sent.push(data);
  }

  close(code = 1000, reason = "") {
    if (this.readyState === FakeWebSocket.CLOSED) return;
    this.readyState = FakeWebSocket.CLOSED;
    this.onclose?.({ code, reason });
  }

  /** The server's session create succeeded. */
  establish() {
    this.readyState = FakeWebSocket.OPEN;
    this.onopen?.();
    this.onmessage?.({ data: JSON.stringify({ type: "init", pid: 4242, session_id: "daemon-1" }) });
  }

  /**
   * The daemon refused the directory. The server sends the coded error, then
   * its handler returns and drops the connection without a close frame (1006).
   */
  refuseWorkingDir(dir: string, code = "working_dir_unavailable") {
    this.fail(code, `create terminal session: terminal working directory unavailable: ${dir} does not exist`);
  }

  /** The server sends a coded error, then drops the connection (1006). */
  fail(code: string, data: string) {
    this.readyState = FakeWebSocket.OPEN;
    this.onopen?.();
    this.onmessage?.({ data: JSON.stringify({ type: "error", code, data }) });
    this.readyState = FakeWebSocket.CLOSED;
    this.onclose?.({ code: 1006, reason: "" });
  }
}

// --- Module mocks (see Terminal.daemonOffline.test.tsx for the rationale) ---

const daemonStatusMock = vi.hoisted(() => vi.fn());

vi.mock("../../../hooks/useDaemonStatus", () => ({
  useDaemonStatus: daemonStatusMock,
}));

const xtermWrites = vi.hoisted(() => [] as string[]);

vi.mock("@xterm/xterm", () => ({
  Terminal: class {
    cols = 80;
    rows = 24;
    options: Record<string, unknown> = {};
    open() {}
    loadAddon() {}
    write(data: string) {
      xtermWrites.push(data);
    }
    writeln(data: string) {
      xtermWrites.push(data);
    }
    clear() {}
    focus() {}
    dispose() {}
    onData() {}
    attachCustomKeyEventHandler() {}
  },
}));

vi.mock("@xterm/addon-fit", () => ({
  FitAddon: class {
    fit() {}
    dispose() {}
  },
}));

vi.mock("@xterm/addon-web-links", () => ({
  WebLinksAddon: class {
    dispose() {}
  },
}));

vi.mock("@xterm/addon-webgl", () => ({
  WebglAddon: class {
    onContextLoss() {}
    dispose() {}
  },
}));

vi.mock("@xterm/xterm/css/xterm.css", () => ({}));

vi.mock("../../../lib/supabase", () => ({
  supabase: { auth: { getSession: async () => ({ data: { session: { access_token: "t" } } }) } },
}));

vi.mock("../../../api/grpc-client", () => ({
  getGRPCBaseURLPublic: () => "http://localhost:8080",
}));

const terminalStoreState = vi.hoisted(() => ({
  updateSessionPID: () => {},
  setDaemonSessionId: () => {},
  killSession: vi.fn(),
  activeSessionId: "session-1",
}));

vi.mock("../../../store/terminalStore", () => ({
  useTerminalStore: (selector: (s: typeof terminalStoreState) => unknown) =>
    selector(terminalStoreState),
}));

const sidebarStoreState = vi.hoisted(() => ({ width: 300, diffHeightPercent: 50 }));

vi.mock("../../../store/sidebarStore", () => ({
  useSidebarStore: (selector: (s: typeof sidebarStoreState) => unknown) =>
    selector(sidebarStoreState),
}));

vi.mock("../../../hooks/useDaemonWait", () => ({
  useDaemonWait: () => ({ state: null, elapsedMs: 0, daemon: null, retryNow: () => {} }),
}));

import { Terminal } from "../Terminal";

const PROJECT_DIR = "/home/workspace/projects/forge";

/** Longer than any retry delay the terminal uses. */
const LONGEST_RETRY_MS = 31_000;

/** connectWebSocket awaits supabase.auth.getSession() before it opens a socket. */
async function flushConnect() {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
}

async function nextSocket(previousCount: number): Promise<FakeWebSocket> {
  await act(async () => {
    vi.advanceTimersByTime(LONGEST_RETRY_MS);
  });
  await flushConnect();
  expect(sockets.length).toBe(previousCount + 1);
  return sockets[sockets.length - 1];
}

function requestedDir(ws: FakeWebSocket): string | null {
  return new URL(ws.url).searchParams.get("workingDir");
}

beforeEach(() => {
  sockets.length = 0;
  xtermWrites.length = 0;
  vi.stubGlobal("WebSocket", FakeWebSocket);
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    },
  );
  vi.useFakeTimers({ shouldAdvanceTime: true });
  daemonStatusMock.mockReturnValue({
    daemons: [],
    activeDaemon: { daemonId: "d1" },
    loading: false,
    refresh: () => {},
  });
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  daemonStatusMock.mockReset();
  cleanup();
});

describe("Terminal working directory not on the machine yet", () => {
  it("says which directory it is waiting for instead of printing an error", async () => {
    render(React.createElement(Terminal, { sessionId: "session-1", workingDir: PROJECT_DIR }));
    await flushConnect();

    await act(async () => {
      sockets[0].refuseWorkingDir(PROJECT_DIR);
    });

    expect(screen.getByText(PROJECT_DIR)).toBeTruthy();
    expect(screen.getByRole("status").textContent).toMatch(/doesn.t exist on this machine yet/);
    // Transient state belongs in the overlay; the scrollback holds shell output.
    expect(xtermWrites.join("")).not.toContain("Error:");
  });

  it("keeps waiting past the reconnect budget, then opens the shell in that directory", async () => {
    render(React.createElement(Terminal, { sessionId: "session-1", workingDir: PROJECT_DIR }));
    await flushConnect();

    // A clone can take minutes. Refuse more times than the ordinary
    // reconnect budget (10 attempts) allows.
    let ws = sockets[0];
    for (let attempt = 0; attempt < 15; attempt++) {
      await act(async () => {
        ws.refuseWorkingDir(PROJECT_DIR);
      });
      ws = await nextSocket(sockets.length);
      expect(requestedDir(ws)).toBe(PROJECT_DIR);
    }
    expect(xtermWrites.join("")).not.toContain("Max reconnect attempts");

    // The clone lands: the next attempt succeeds and the overlay goes away.
    await act(async () => {
      ws.establish();
    });
    expect(screen.queryByText(PROJECT_DIR)).toBeNull();
    expect(screen.queryByRole("status")).toBeNull();
  });
});

describe("Terminal working directory gone for good", () => {
  // Prod, 2026-10-08: a hidden terminal for a removed worktree retried every
  // ~12s for five hours, because "not there yet" and "never coming back" were
  // the same server code. The server now says working_dir_missing for the
  // second; the terminal must stop and say so.
  const REMOVED_DIR = "/home/workspace/.reliant/worktrees/reliant-labs/365-95b57228";

  it("stops retrying and offers to close the terminal", async () => {
    render(React.createElement(Terminal, { sessionId: "session-1", workingDir: REMOVED_DIR }));
    await flushConnect();

    await act(async () => {
      sockets[0].refuseWorkingDir(REMOVED_DIR, "working_dir_missing");
    });

    expect(screen.getByText("This folder no longer exists")).toBeTruthy();
    expect(screen.getByText(REMOVED_DIR)).toBeTruthy();

    await act(async () => {
      vi.advanceTimersByTime(LONGEST_RETRY_MS * 5);
    });
    await flushConnect();
    expect(sockets).toHaveLength(1);
    expect(xtermWrites.join("")).not.toContain("Error:");

    fireEvent.click(screen.getByRole("button", { name: "Close terminal" }));
    expect(terminalStoreState.killSession).toHaveBeenCalledWith("session-1");
  });
});

describe("Terminal machine not serving", () => {
  it("reads the typed daemon_unavailable code, not the prose", async () => {
    render(React.createElement(Terminal, { sessionId: "session-1", workingDir: "/w" }));
    await flushConnect();

    await act(async () => {
      sockets[0].fail("daemon_unavailable", "your machine is asleep");
    });

    // A wait, not a red error line in the user's scrollback.
    expect(xtermWrites.join("")).not.toContain("Error:");
  });
});

describe("Terminal with no machine on the account", () => {
  // The server reports "no machine at all" under the same daemon_unavailable
  // code as a machine that is asleep. Treated as a wait, the terminal showed
  // "Waiting for your machine" and retried forever for a machine that does
  // not exist. It must say there is none, offer to connect one, and stop.
  const NO_MACHINE =
    "create terminal session: no daemon available: no machine is connected to your account yet";

  it("offers to connect a machine and stops retrying", async () => {
    daemonStatusMock.mockReturnValue({ daemons: [], activeDaemon: undefined, loading: false, refresh: () => {} });
    render(React.createElement(Terminal, { sessionId: "session-1", workingDir: "/w" }));
    await flushConnect();

    await act(async () => {
      sockets[0].fail("daemon_unavailable", NO_MACHINE);
    });

    expect(screen.getByTestId("no-machine-state").textContent).toContain("No machine connected");
    expect(screen.getByRole("button", { name: "Connect a machine" })).toBeTruthy();

    await act(async () => {
      vi.advanceTimersByTime(LONGEST_RETRY_MS * 5);
    });
    await flushConnect();
    expect(sockets).toHaveLength(1);
    expect(xtermWrites.join("")).not.toContain("Error:");
  });

  it("connects by itself once a machine appears", async () => {
    daemonStatusMock.mockReturnValue({ daemons: [], activeDaemon: undefined, loading: false, refresh: () => {} });
    const view = render(React.createElement(Terminal, { sessionId: "session-1", workingDir: "/w" }));
    await flushConnect();
    await act(async () => {
      sockets[0].fail("no_machine", NO_MACHINE);
    });

    daemonStatusMock.mockReturnValue({ daemons: [], activeDaemon: { daemonId: "d1" }, loading: false, refresh: () => {} });
    view.rerender(React.createElement(Terminal, { sessionId: "session-1", workingDir: "/w" }));
    await flushConnect();

    expect(sockets).toHaveLength(2);
    expect(screen.queryByTestId("no-machine-state")).toBeNull();
  });
});
