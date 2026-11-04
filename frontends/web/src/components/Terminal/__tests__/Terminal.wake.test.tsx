import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, render, screen } from "@testing-library/react";
import React from "react";
import { resetPageActivityForTests } from "../../../lib/pageActivity";

// ---------------------------------------------------------------------------
// After a laptop sleeps the browser can keep a dead socket OPEN until TCP
// notices (~2 minutes in prod), a stale daemon status sends the retry down the
// 30s path, and a long absence spends the whole attempt budget so only a manual
// Reconnect is left. A wake signal (tab visible again, `online`, bfcache
// restore) must recover at once: reconnect a closed socket immediately, probe an
// OPEN one and replace it when silent, and hand back an exhausted budget —
// without ever running two connections at once.
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

  url: string;

  constructor(url: string) {
    this.url = url;
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

  /** The connection drops without a close frame (1006). */
  drop() {
    this.readyState = FakeWebSocket.CLOSED;
    this.onclose?.({ code: 1006, reason: "" });
  }

  /** The server answers a liveness ping. */
  pong() {
    this.onmessage?.({ data: JSON.stringify({ type: "pong" }) });
  }

  get pings() {
    return this.sent.filter((m) => m.includes('"ping"')).length;
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

vi.mock("@/lib/connect", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/connect")>()),
  reliantApiBaseUrl: () => "http://localhost:8080",
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

const refreshMock = vi.fn();

/** connectWebSocket awaits supabase.auth.getSession() before it opens a socket. */
async function flushConnect() {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
}

function setVisibility(state: "hidden" | "visible") {
  Object.defineProperty(document, "visibilityState", { configurable: true, get: () => state });
  document.dispatchEvent(new Event("visibilitychange"));
}

/**
 * The tab goes to the background for `ms` and comes back. The clock jumps
 * without firing timers: a sleeping page runs none, and the backoff timer that
 * was pending when it slept has not fired when it wakes.
 */
async function sleepAndWake(ms = 60_000) {
  await act(async () => {
    setVisibility("hidden");
  });
  vi.setSystemTime(Date.now() + ms);
  await act(async () => {
    setVisibility("visible");
  });
  await flushConnect();
}

async function mountConnected() {
  render(React.createElement(Terminal, { sessionId: "session-1", workingDir: "/work" }));
  await flushConnect();
  expect(sockets.length).toBe(1);
  await act(async () => {
    sockets[0].establish();
  });
  return sockets[0];
}

beforeEach(() => {
  sockets.length = 0;
  xtermWrites.length = 0;
  refreshMock.mockReset();
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
  resetPageActivityForTests();
  daemonStatusMock.mockReturnValue({
    daemons: [],
    activeDaemon: { daemonId: "d1" },
    loading: false,
    refresh: refreshMock,
  });
});

afterEach(() => {
  cleanup();
  resetPageActivityForTests();
  Object.defineProperty(document, "visibilityState", { configurable: true, get: () => "visible" });
  vi.useRealTimers();
  vi.unstubAllGlobals();
  daemonStatusMock.mockReset();
});

describe("Terminal wake recovery", () => {
  it("reconnects at once when the tab returns and the socket is already closed", async () => {
    const first = await mountConnected();

    // The socket dies while the page sleeps; its backoff timer is throttled
    // far past the user's return.
    await act(async () => {
      first.drop();
    });
    expect(sockets.length).toBe(1);

    await sleepAndWake();

    expect(sockets.length).toBe(2);
    expect(refreshMock).toHaveBeenCalled();
  });

  it("probes an OPEN socket and replaces it when nothing comes back", async () => {
    const first = await mountConnected();

    await sleepAndWake();
    expect(first.pings).toBe(1);
    expect(sockets.length).toBe(1);

    await act(async () => {
      vi.advanceTimersByTime(4_500);
    });
    await flushConnect();

    expect(sockets.length).toBe(2);
    expect(first.readyState).toBe(FakeWebSocket.CLOSED);
  });

  it("keeps an OPEN socket that answers the probe", async () => {
    const first = await mountConnected();

    await sleepAndWake();
    expect(first.pings).toBe(1);
    await act(async () => {
      first.pong();
    });
    await act(async () => {
      vi.advanceTimersByTime(10_000);
    });
    await flushConnect();

    expect(sockets.length).toBe(1);
    expect(first.readyState).toBe(FakeWebSocket.OPEN);
    expect(screen.queryByRole("status")).toBeNull();
  });

  it("ignores a tab flick shorter than a real absence", async () => {
    const first = await mountConnected();

    await sleepAndWake(500);

    expect(first.pings).toBe(0);
    expect(sockets.length).toBe(1);
  });

  it("hands back an exhausted attempt budget when the network returns", async () => {
    render(React.createElement(Terminal, { sessionId: "session-1", workingDir: "/work" }));
    await flushConnect();

    // Every attempt drops before init: the 10-attempt budget runs out.
    for (let i = 0; i < 11; i++) {
      await act(async () => {
        sockets[sockets.length - 1].drop();
      });
      await act(async () => {
        vi.advanceTimersByTime(25_000);
      });
      await flushConnect();
    }
    await act(async () => {
      sockets[sockets.length - 1].drop();
    });
    expect(screen.getByText("Terminal disconnected")).toBeTruthy();
    const before = sockets.length;

    await act(async () => {
      window.dispatchEvent(new Event("online"));
    });
    await flushConnect();

    expect(sockets.length).toBe(before + 1);
    expect(screen.queryByText("Terminal disconnected")).toBeNull();
    await act(async () => {
      sockets[sockets.length - 1].establish();
    });
    expect(screen.queryByRole("status")).toBeNull();
  });

  it("does not go down the 30s path when the daemon status is stale", async () => {
    daemonStatusMock.mockReturnValue({
      daemons: [],
      activeDaemon: undefined,
      loading: false,
      refresh: refreshMock,
    });
    render(React.createElement(Terminal, { sessionId: "session-1", workingDir: "/work" }));
    await flushConnect();
    await act(async () => {
      sockets[0].drop();
    });
    expect(sockets.length).toBe(1);

    await act(async () => {
      window.dispatchEvent(new Event("online"));
    });
    await flushConnect();

    expect(sockets.length).toBe(2);
    expect(refreshMock).toHaveBeenCalled();
  });

  it("opens one connection for a burst of wake signals", async () => {
    const first = await mountConnected();
    await act(async () => {
      first.drop();
    });

    await act(async () => {
      setVisibility("hidden");
    });
    vi.setSystemTime(Date.now() + 60_000);
    await act(async () => {
      setVisibility("visible");
      window.dispatchEvent(new Event("online"));
      window.dispatchEvent(new Event("online"));
    });
    await flushConnect();

    expect(sockets.length).toBe(2);
  });

  it("does not respawn a shell the user exited", async () => {
    const first = await mountConnected();
    await act(async () => {
      first.close(1000, "");
    });
    expect(screen.getByText("Terminal disconnected")).toBeTruthy();

    await act(async () => {
      window.dispatchEvent(new Event("online"));
    });
    await flushConnect();

    expect(sockets.length).toBe(1);
  });
});
