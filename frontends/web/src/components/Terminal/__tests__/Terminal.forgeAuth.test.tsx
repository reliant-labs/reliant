import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, render } from "@testing-library/react";
import React from "react";

// ---------------------------------------------------------------------------
// forge sign-in (AUTH_PROVIDER "forge"): the session is an HttpOnly cookie on
// this page's origin, so the terminal socket goes to reliant-api through
// `/_api/@reliant` (reliantApiBaseUrl) and carries NO `?token=`; the cookie
// rides the same-origin upgrade. Supabase mode still sends its token.
// ---------------------------------------------------------------------------

const sockets: { url: string }[] = [];

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
  url: string;
  constructor(url: string) {
    this.url = url;
    sockets.push(this);
  }
  send() {}
  close() {
    this.readyState = FakeWebSocket.CLOSED;
  }
}

const state = vi.hoisted(() => ({ forge: false, base: "" }));
const getSession = vi.hoisted(() => vi.fn());

vi.mock("@/lib/auth/forge-api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/auth/forge-api")>()),
  isForgeAuth: () => state.forge,
}));
vi.mock("@/lib/connect", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/connect")>()),
  reliantApiBaseUrl: () => state.base,
}));
vi.mock("../../../lib/supabase", () => ({ supabase: { auth: { getSession } } }));

vi.mock("../../../hooks/useDaemonStatus", () => ({
  useDaemonStatus: () => ({ daemons: [], activeDaemon: { daemonId: "d1" }, loading: false, refresh: () => {} }),
}));
vi.mock("../../../hooks/useDaemonWait", () => ({
  useDaemonWait: () => ({ state: null, elapsedMs: 0, daemon: null, retryNow: () => {} }),
}));
vi.mock("@xterm/xterm", () => ({
  Terminal: class {
    cols = 80;
    rows = 24;
    options: Record<string, unknown> = {};
    open() {}
    loadAddon() {}
    write() {}
    writeln() {}
    clear() {}
    focus() {}
    dispose() {}
    onData() {}
    attachCustomKeyEventHandler() {}
  },
}));
vi.mock("@xterm/addon-fit", () => ({ FitAddon: class { fit() {} dispose() {} } }));
vi.mock("@xterm/addon-web-links", () => ({ WebLinksAddon: class { dispose() {} } }));
vi.mock("@xterm/addon-webgl", () => ({ WebglAddon: class { onContextLoss() {} dispose() {} } }));
vi.mock("@xterm/xterm/css/xterm.css", () => ({}));

const terminalStoreState = vi.hoisted(() => ({
  updateSessionPID: () => {},
  setDaemonSessionId: () => {},
  setSessionMachine: () => {},
  killSession: () => {},
  activeSessionId: "session-1",
}));
vi.mock("../../../store/terminalStore", () => ({
  useTerminalStore: (selector: (s: typeof terminalStoreState) => unknown) => selector(terminalStoreState),
}));
const sidebarStoreState = vi.hoisted(() => ({ width: 300, diffHeightPercent: 50 }));
vi.mock("../../../store/sidebarStore", () => ({
  useSidebarStore: (selector: (s: typeof sidebarStoreState) => unknown) => selector(sidebarStoreState),
}));

import { Terminal } from "../Terminal";

async function openSocket(): Promise<URL> {
  render(React.createElement(Terminal, { sessionId: "session-1", workingDir: "/w", chatId: "chat-1" }));
  await act(async () => {
    for (let i = 0; i < 5 && sockets.length === 0; i++) await Promise.resolve();
  });
  expect(sockets).toHaveLength(1);
  return new URL(sockets[0].url);
}

beforeEach(() => {
  sockets.length = 0;
  getSession.mockReset().mockResolvedValue({ data: { session: { access_token: "jwt-1" } } });
  vi.stubGlobal("WebSocket", FakeWebSocket);
  vi.stubGlobal("ResizeObserver", class { observe() {} unobserve() {} disconnect() {} });
});

afterEach(() => {
  state.forge = false;
  vi.unstubAllGlobals();
  cleanup();
});

describe("Terminal socket auth", () => {
  it("forge sign-in: connects under /_api/@reliant (wss) with no token", async () => {
    state.forge = true;
    state.base = "https://app.example.test/_api/@reliant";
    const url = await openSocket();
    expect(`${url.protocol}//${url.host}${url.pathname}`).toBe("wss://app.example.test/_api/@reliant/api/v2/terminal/ws");
    expect(url.searchParams.has("token")).toBe(false);
    expect(url.searchParams.get("chatId")).toBe("chat-1");
    expect(getSession).not.toHaveBeenCalled();
  });

  it("supabase sign-in: still sends the session token", async () => {
    state.base = "http://localhost:8080";
    const url = await openSocket();
    expect(`${url.protocol}//${url.host}${url.pathname}`).toBe("ws://localhost:8080/api/v2/terminal/ws");
    expect(url.searchParams.get("token")).toBe("jwt-1");
  });
});
