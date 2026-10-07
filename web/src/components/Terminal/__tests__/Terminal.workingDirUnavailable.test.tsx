import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, render, screen } from "@testing-library/react";
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
  refuseWorkingDir(dir: string) {
    this.readyState = FakeWebSocket.OPEN;
    this.onopen?.();
    this.onmessage?.({
      data: JSON.stringify({
        type: "error",
        code: "working_dir_unavailable",
        data: `create terminal session: terminal working directory unavailable: ${dir} does not exist`,
      }),
    });
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
