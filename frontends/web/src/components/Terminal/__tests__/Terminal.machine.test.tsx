import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import React from "react";

// ---------------------------------------------------------------------------
// Prod, 2026-11-04: a terminal opened beside a chat ran on a different
// machine from the chat's, and nothing on screen said so — `ls` of a folder
// the chat had just written answered "No such file or directory". Opening
// another terminal on the chat's machine (load average ~220) then sat on
// "Connecting to terminal..." with no end.
//
// These tests pin what the user now sees: which machine the terminal is on,
// a machine that did not answer named with a retry rather than a spinner, a
// machine without the project said in the server's words, and a bound on
// every connection attempt.
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
  closed = false;

  url: string;

  constructor(url: string) {
    this.url = url;
    sockets.push(this);
  }

  send() {}

  close(code = 1000, reason = "") {
    this.closed = true;
    if (this.readyState === FakeWebSocket.CLOSED) return;
    this.readyState = FakeWebSocket.CLOSED;
    this.onclose?.({ code, reason });
  }

  /** The server's session create succeeded on `machine`. */
  establish(machine: { daemon_id: string; machine_name: string }) {
    this.readyState = FakeWebSocket.OPEN;
    this.onopen?.();
    this.onmessage?.({ data: JSON.stringify({ type: "init", pid: 4242, session_id: "daemon-1", ...machine }) });
  }

  /** The server sends a coded error naming the machine, then drops the connection (1006). */
  fail(code: string, data: string, machine: { daemon_id: string; machine_name: string }) {
    this.readyState = FakeWebSocket.OPEN;
    this.onopen?.();
    this.onmessage?.({ data: JSON.stringify({ type: "error", code, data, ...machine }) });
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

vi.mock("@/lib/connect", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/connect")>()),
  reliantApiBaseUrl: () => "http://localhost:8080",
}));

const terminalStoreState = vi.hoisted(() => ({
  updateSessionPID: () => {},
  setDaemonSessionId: () => {},
  setSessionMachine: vi.fn(),
  killSession: vi.fn(),
  activeSessionId: "session-1",
}));

vi.mock("../../../store/terminalStore", () => ({
  useTerminalStore: (selector: (s: typeof terminalStoreState) => unknown) => selector(terminalStoreState),
}));

const sidebarStoreState = vi.hoisted(() => ({ width: 300, diffHeightPercent: 50 }));

vi.mock("../../../store/sidebarStore", () => ({
  useSidebarStore: (selector: (s: typeof sidebarStoreState) => unknown) => selector(sidebarStoreState),
}));

vi.mock("../../../hooks/useDaemonWait", () => ({
  useDaemonWait: () => ({ state: null, elapsedMs: 0, daemon: null, retryNow: () => {} }),
}));

import { Terminal, TERMINAL_CONNECT_TIMEOUT_MS } from "../Terminal";

const CICD = { daemon_id: "e164d47d-1288-468d-ad0b-c487af2da1c4", machine_name: "cicd" };

/** Longer than any retry delay the terminal uses. */
const LONGEST_RETRY_MS = 31_000;

/** connectWebSocket awaits supabase.auth.getSession() before it opens a socket. */
async function flushConnect() {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
}

beforeEach(() => {
  sockets.length = 0;
  xtermWrites.length = 0;
  terminalStoreState.setSessionMachine.mockClear();
  terminalStoreState.killSession.mockClear();
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
  // The daemon list has not loaded the terminal's machine: the server's name
  // is what the terminal shows.
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

function renderTerminal() {
  return render(React.createElement(Terminal, { sessionId: "session-1", workingDir: "/w", chatId: "chat-1" }));
}

describe("Terminal says which machine it is on", () => {
  it("records the machine the server opened it on", async () => {
    renderTerminal();
    await flushConnect();
    expect(new URL(sockets[0].url).searchParams.get("chatId")).toBe("chat-1");

    await act(async () => {
      sockets[0].establish(CICD);
    });

    expect(terminalStoreState.setSessionMachine).toHaveBeenCalledWith("session-1", CICD.daemon_id, "cicd");
  });

  it("names the machine a missing folder was looked for on", async () => {
    renderTerminal();
    await flushConnect();

    await act(async () => {
      sockets[0].fail("working_dir_missing", "terminal working directory unavailable: /w does not exist", CICD);
    });

    expect(screen.getByRole("status").textContent).toContain("It isn\u2019t on cicd");
  });
});

describe("Terminal machine that does not answer", () => {
  it("names the machine and waits for the user instead of retrying on a timer", async () => {
    renderTerminal();
    await flushConnect();

    await act(async () => {
      sockets[0].fail("machine_not_answering", "create terminal session: the machine did not answer in time", CICD);
    });

    expect(screen.getByRole("alert").textContent).toContain("cicd isn't answering");
    expect(xtermWrites.join("")).not.toContain("Error:");

    await act(async () => {
      vi.advanceTimersByTime(LONGEST_RETRY_MS * 3);
    });
    await flushConnect();
    expect(sockets).toHaveLength(1);

    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    await flushConnect();
    expect(sockets).toHaveLength(2);
  });
});

describe("Terminal machine without the project", () => {
  it("shows the server's sentence and offers to retry or close", async () => {
    const sentence =
      '"reliant-labs" isn\'t checked out on "cloud-2xl": switch to "default", which has it, or clone it onto "cloud-2xl" to open a terminal in it';
    renderTerminal();
    await flushConnect();

    await act(async () => {
      sockets[0].fail("checkout_missing", sentence, { daemon_id: "07191837", machine_name: "cloud-2xl" });
    });

    const alert = screen.getByRole("alert");
    expect(alert.textContent).toContain("This project isn't on cloud-2xl");
    expect(alert.textContent).toContain(sentence);

    await act(async () => {
      vi.advanceTimersByTime(LONGEST_RETRY_MS * 3);
    });
    await flushConnect();
    expect(sockets).toHaveLength(1);

    fireEvent.click(screen.getByRole("button", { name: "Close terminal" }));
    expect(terminalStoreState.killSession).toHaveBeenCalledWith("session-1");
  });
});

describe("Terminal connection that never answers", () => {
  it("gives up after the bound, says so, and retries on request", async () => {
    renderTerminal();
    await flushConnect();
    expect(sockets).toHaveLength(1);
    expect(screen.getByRole("status").textContent).toContain("Connecting to terminal");

    // The socket opens but the server never sends "init" or "error".
    await act(async () => {
      sockets[0].readyState = FakeWebSocket.OPEN;
      sockets[0].onopen?.();
      vi.advanceTimersByTime(TERMINAL_CONNECT_TIMEOUT_MS + 1);
    });

    expect(sockets[0].closed).toBe(true);
    expect(screen.getByRole("alert").textContent).toContain("Couldn\u2019t open the terminal");
    expect(screen.queryByText(/Connecting to terminal/)).toBeNull();

    // Nothing reconnects by itself...
    await act(async () => {
      vi.advanceTimersByTime(LONGEST_RETRY_MS * 3);
    });
    await flushConnect();
    expect(sockets).toHaveLength(1);

    // ...and Try again does, and succeeds.
    fireEvent.click(screen.getByRole("button", { name: "Try again" }));
    await flushConnect();
    expect(sockets).toHaveLength(2);
    await act(async () => {
      sockets[1].establish(CICD);
    });
    expect(screen.queryByRole("alert")).toBeNull();
  });

  it("does not time out a terminal that answered", async () => {
    renderTerminal();
    await flushConnect();
    await act(async () => {
      sockets[0].establish(CICD);
      vi.advanceTimersByTime(TERMINAL_CONNECT_TIMEOUT_MS * 2);
    });

    expect(sockets[0].closed).toBe(false);
    expect(screen.queryByRole("alert")).toBeNull();
  });
});
