import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, render, screen } from "@testing-library/react";
import React from "react";

// ---------------------------------------------------------------------------
// The terminal's websocket is to the api-server, not to the daemon. So when the
// daemon dies the socket stays OPEN — ws.onclose never fires, and nothing moves
// the connection state machine out of "connected". The terminal went on showing
// a healthy prompt for a machine that was gone, while chat, ConnectDaemonModal
// and DaemonConnectingGate all rendered the offline state correctly. Only a
// refresh corrected it, because a remount re-evaluates against an
// already-offline daemon.
//
// These tests drive the daemon-status hook directly and assert on what the user
// sees, so they pin the user-visible contract rather than the internal state
// name: an offline daemon must produce the waiting overlay WITHOUT any socket
// close, and a poll blip must NOT tear down a live session.
// ---------------------------------------------------------------------------

/** Sockets opened during a test, so assertions can inspect them. */
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
  closeCalls = 0;
  sent: string[] = [];

  constructor(public url: string) {
    sockets.push(this);
  }

  send(data: string) {
    this.sent.push(data);
  }

  close(code = 1000, reason = "") {
    this.closeCalls += 1;
    if (this.readyState === FakeWebSocket.CLOSED) return;
    this.readyState = FakeWebSocket.CLOSED;
    this.onclose?.({ code, reason });
  }

  /** Drive the server side: open the socket and confirm the session. */
  establish() {
    this.readyState = FakeWebSocket.OPEN;
    this.onopen?.();
    this.onmessage?.({ data: JSON.stringify({ type: "init", pid: 4242, session_id: "daemon-1" }) });
  }
}

// --- Module mocks ----------------------------------------------------------
// Terminal.tsx pulls in xterm (canvas/WebGL), supabase, the gRPC client and two
// zustand stores. None of that is what these tests are about, so each is
// reduced to the smallest thing that lets the component mount in jsdom.

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

// The store selections must return STABLE identities across renders. The main
// terminal effect lists updateSessionPID/setDaemonSessionId in its dependency
// array, so a mock that builds fresh closures per render tears the session down
// and reopens the socket on every re-render — which would mask exactly the
// behaviour under test.
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

// The overlay's copy comes from the shared daemon-wait policy, which polls the
// control plane. Stub the hook to a fixed state so the assertions are about
// WHETHER the overlay renders, not about which escalation tier it picked.
vi.mock("../../../hooks/useDaemonWait", () => ({
  useDaemonWait: ({ waiting }: { waiting: boolean }) => ({
    state: waiting
      ? {
          tone: "waiting",
          title: "Waiting for your machine",
          detail: null,
          reason: null,
          shouldRetry: true,
          showRetry: false,
          showManage: false,
        }
      : null,
    elapsedMs: 0,
    daemon: null,
    retryNow: () => {},
  }),
}));

import { Terminal } from "../Terminal";

/** The grace period the component waits before acting on an offline daemon. */
const GRACE_MS = 6000;

function setDaemonOnline(online: boolean) {
  daemonStatusMock.mockReturnValue({
    daemons: [],
    activeDaemon: online ? { daemonId: "d1" } : undefined,
    loading: false,
    refresh: () => {},
  });
}

/** Mount, let the async connect settle, and bring the session up. */
async function mountConnected() {
  const view = render(React.createElement(Terminal, { sessionId: "session-1" }));
  // connectWebSocket awaits supabase.auth.getSession() before constructing the
  // socket, so the socket does not exist until microtasks drain.
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
  const ws = sockets.at(-1);
  if (!ws) throw new Error("no websocket was opened");
  await act(async () => {
    ws.establish();
  });
  return { view, ws };
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
  setDaemonOnline(true);
});

afterEach(() => {
  vi.useRealTimers();
  vi.unstubAllGlobals();
  daemonStatusMock.mockReset();
  cleanup();
});

describe("Terminal daemon online -> offline", () => {
  it("shows the waiting overlay when the daemon goes offline, even though the socket never closes", async () => {
    const { view, ws } = await mountConnected();

    // Baseline: a live session shows no overlay.
    expect(screen.queryByText("Waiting for your machine")).toBeNull();

    // The daemon dies. The socket is to the server, so it stays OPEN — this is
    // the exact condition under which the bug produced a permanently
    // "connected" terminal.
    setDaemonOnline(false);
    view.rerender(React.createElement(Terminal, { sessionId: "session-1" }));

    await act(async () => {
      vi.advanceTimersByTime(GRACE_MS + 100);
      await Promise.resolve();
    });

    expect(ws.readyState).toBe(FakeWebSocket.CLOSED);
    expect(screen.getByText("Waiting for your machine")).toBeTruthy();
  });

  it("does not write transient daemon state into the scrollback", async () => {
    const { view } = await mountConnected();

    setDaemonOnline(false);
    view.rerender(React.createElement(Terminal, { sessionId: "session-1" }));

    await act(async () => {
      vi.advanceTimersByTime(GRACE_MS + 100);
      await Promise.resolve();
    });

    // The overlay owns transient state; the buffer holds session output. In
    // particular our own close() reports code 1000, which must NOT be
    // mistaken for the shell exiting.
    const buffer = xtermWrites.join("");
    expect(buffer).not.toContain("Session ended");
    expect(buffer).not.toContain("Waiting");
  });

  it("leaves a live session alone when the daemon reappears within the grace period", async () => {
    const { view, ws } = await mountConnected();

    // A single poll missing the daemon is common and self-healing — a daemon
    // reconnecting flips to DISCONNECTED for a tick on the way through.
    setDaemonOnline(false);
    view.rerender(React.createElement(Terminal, { sessionId: "session-1" }));

    await act(async () => {
      vi.advanceTimersByTime(GRACE_MS / 2);
      await Promise.resolve();
    });

    setDaemonOnline(true);
    view.rerender(React.createElement(Terminal, { sessionId: "session-1" }));

    await act(async () => {
      vi.advanceTimersByTime(GRACE_MS * 2);
      await Promise.resolve();
    });

    // Session survived: socket never closed, no overlay, no extra reconnect.
    expect(ws.closeCalls).toBe(0);
    expect(ws.readyState).toBe(FakeWebSocket.OPEN);
    expect(screen.queryByText("Waiting for your machine")).toBeNull();
    expect(sockets).toHaveLength(1);
  });
});
