import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { act, cleanup, fireEvent, render, screen } from "@testing-library/react";
import React from "react";
import { WorktreeStatus } from "../../../gen/reliant/v1/worktree_pb";
import type { Worktree } from "../../../store/worktreeStore";

// ---------------------------------------------------------------------------
// "I start typing in a chat on a new workspace, it can take ~10s for the
// terminal to connect, then when it does it steals the cursor focus, and I
// immediately start typing there."
//
// A new workspace opens with the terminal panel open and no directory yet. The
// panel creates the session when the checkout lands, and the terminal used to
// focus itself whenever its session became active — so the cursor jumped out of
// the composer mid-sentence, with nobody having asked for the terminal.
//
// These tests drive the real panel, terminal and stores, with an xterm stand-in
// whose focus() really moves document.activeElement, and assert on where the
// cursor is. The contract: the terminal takes focus only when the user asked
// for it, and a request made before the terminal exists is honoured only while
// the user is still waiting for it.
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

  constructor(public url: string) {
    sockets.push(this);
  }

  send() {}

  close() {
    this.readyState = FakeWebSocket.CLOSED;
  }

  /** Drive the server side: open the socket and confirm the session. */
  establish() {
    this.readyState = FakeWebSocket.OPEN;
    this.onopen?.();
    this.onmessage?.({ data: JSON.stringify({ type: "init", pid: 4242, session_id: "daemon-1" }) });
  }

  /** Drive the server side: the checkout is not on disk yet, so the daemon refuses the directory. */
  refuseWorkingDir() {
    this.readyState = FakeWebSocket.OPEN;
    this.onopen?.();
    this.onmessage?.({
      data: JSON.stringify({ type: "error", code: "working_dir_unavailable", data: "not on the machine yet" }),
    });
    this.readyState = FakeWebSocket.CLOSED;
    this.onclose?.({ code: 1000, reason: "" });
  }
}

vi.mock("../../../api/worktree-grpc", () => ({
  worktreeGrpc: { list: vi.fn(), create: vi.fn() },
}));

vi.mock("../../../api/terminal-grpc", () => ({
  terminalApi: { closeSession: vi.fn().mockResolvedValue({ success: true, message: "" }) },
}));

// xterm renders to canvas, which jsdom cannot do. The stand-in keeps the one
// thing these tests are about: xterm takes input through a hidden textarea, so
// focusing the terminal moves document.activeElement there.
vi.mock("@xterm/xterm", () => ({
  Terminal: class {
    cols = 80;
    rows = 24;
    options: Record<string, unknown> = {};
    private root: HTMLElement | null = null;
    private textarea: HTMLTextAreaElement | null = null;
    open(parent: HTMLElement) {
      this.root = document.createElement("div");
      this.root.className = "xterm";
      this.textarea = document.createElement("textarea");
      this.textarea.className = "xterm-helper-textarea";
      this.root.appendChild(this.textarea);
      parent.appendChild(this.root);
    }
    focus() {
      this.textarea?.focus();
    }
    dispose() {
      this.root?.remove();
    }
    loadAddon() {}
    write() {}
    writeln() {}
    clear() {}
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

vi.mock("../../../api/grpc-client", async (importOriginal) => ({
  ...(await importOriginal<typeof import("../../../api/grpc-client")>()),
  getGRPCBaseURLPublic: () => "http://localhost:8080",
}));

vi.mock("../../../hooks/useDaemonStatus", () => ({
  useDaemonStatus: () => ({
    daemons: [],
    activeDaemon: { daemonId: "d1" },
    loading: false,
    refresh: () => {},
  }),
}));

vi.mock("../../../hooks/useDaemonWait", () => ({
  useDaemonWait: () => ({ state: null, elapsedMs: 0, daemon: null, retryNow: () => {} }),
}));

import { TerminalPanel } from "../TerminalPanel";
import { useWorktreeStore } from "../../../store/worktreeStore";
import { useTerminalStore } from "../../../store/terminalStore";
import { useProjectStore } from "../../../store/projectStore";

const projectId = "project-1";
const projectPath = "/home/u/src/proj";
const workspacePath = "/home/u/.reliant/worktrees/proj/triggers-9c37c238";
const now = "2026-01-01T00:00:00.000Z";

function worktree(overrides: Partial<Worktree>): Worktree {
  return {
    id: "wt-main",
    name: "Main workspace",
    path: projectPath,
    branch: "main",
    base_branch: "main",
    project_id: projectId,
    status: WorktreeStatus.ACTIVE,
    is_main: true,
    created_at: now,
    updated_at: now,
    last_active: now,
    deleted_at: null,
    ...overrides,
  };
}

const mainWorktree = worktree({});
const creatingWorktree = worktree({
  id: "wt-new",
  name: "triggers",
  path: "",
  branch: "triggers",
  is_main: false,
  status: WorktreeStatus.CREATING,
});

/** The chat composer, next to the panel, as in the app. */
function renderApp() {
  render(
    <>
      <textarea aria-label="Message" />
      <TerminalPanel />
    </>,
  );
  return screen.getByLabelText("Message");
}

/** The workspace's checkout lands: the server reports its path. */
function settleNewWorkspace() {
  const settled = { ...creatingWorktree, path: workspacePath, status: WorktreeStatus.ACTIVE };
  act(() => {
    useWorktreeStore.setState({ worktrees: [mainWorktree, settled], currentWorktree: settled });
  });
}

/** Let the newest terminal open its socket, and bring its session up. */
async function connectNewestTerminal() {
  // connectWebSocket awaits the auth session before it constructs the socket.
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
  const ws = sockets.at(-1);
  if (!ws) throw new Error("no websocket was opened");
  await act(async () => {
    ws.establish();
  });
}

/** Let the newest terminal open its socket, without bringing its session up. */
async function openNewestSocket() {
  await act(async () => {
    await Promise.resolve();
    await Promise.resolve();
  });
  const ws = sockets.at(-1);
  if (!ws) throw new Error("no websocket was opened");
  return ws;
}

/**
 * Text arriving in `el` with no keydown: macOS dictation, an IME commit, paste
 * from the context menu, a drop. The browser still fires beforeinput for all of
 * them.
 */
function enterTextWithoutKeys(el: HTMLElement, data: string) {
  fireEvent(el, new InputEvent("beforeinput", { bubbles: true, cancelable: true, inputType: "insertText", data }));
}

/** One macrotask: what separates the action that asked for focus from what the user does next. */
async function nextTask() {
  await act(async () => {
    await new Promise((resolve) => setTimeout(resolve, 0));
  });
}

/** Whether the cursor is in the terminal the panel is showing. */
function visibleTerminalHasFocus() {
  const el = document.activeElement;
  return !!el && el.classList.contains("xterm-helper-textarea") && !el.closest(".hidden");
}

function sessionFor(worktreeId: string, id: string, workingDir: string) {
  return {
    id,
    title: id,
    workingDir,
    projectId,
    worktreeId,
    createdAt: new Date(now),
    isActive: true,
  };
}

beforeEach(() => {
  sockets.length = 0;
  vi.stubGlobal("WebSocket", FakeWebSocket);
  vi.stubGlobal(
    "ResizeObserver",
    class {
      observe() {}
      unobserve() {}
      disconnect() {}
    },
  );
  useWorktreeStore.getState().reset();
  useTerminalStore.getState().hideTerminal();
  useTerminalStore.setState({
    sessions: [],
    activeSessionId: null,
    activeSessionPerWorktree: {},
  });
  useProjectStore.setState({
    currentProject: { id: projectId, path: projectPath } as never,
  });
  useWorktreeStore.setState({
    worktrees: [mainWorktree, creatingWorktree],
    currentWorktree: creatingWorktree,
    hasLoaded: true,
  });
});

afterEach(() => {
  cleanup();
  vi.unstubAllGlobals();
});

describe("terminal focus is never taken without the user asking", () => {
  it("keeps the cursor in the composer when a new workspace's terminal appears and connects", async () => {
    // A new workspace restores its panel open (open by default); nobody asked
    // for the terminal.
    useTerminalStore.getState().restoreOpen(true);
    const composer = renderApp();
    composer.focus();
    fireEvent.keyDown(composer, { key: "h" });

    settleNewWorkspace();
    expect(useTerminalStore.getState().sessions).toHaveLength(1);
    await connectNewestTerminal();

    expect(document.activeElement).toBe(composer);
  });

  it("keeps the cursor in the composer when the terminal waits for the folder and then connects", async () => {
    // The usual shape on a new workspace: the session exists as soon as the
    // path is known, the daemon refuses the directory until the checkout is
    // on disk, and the shell opens on a later retry — the "terminal connects"
    // moment, while the user is mid-sentence.
    useTerminalStore.getState().restoreOpen(true);
    const composer = renderApp();
    composer.focus();
    fireEvent.keyDown(composer, { key: "h" });

    settleNewWorkspace();
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });
    try {
      const first = await openNewestSocket();
      await act(async () => first.refuseWorkingDir());
      expect(screen.getByText(/waiting for this folder/i)).toBeTruthy();

      fireEvent.keyDown(composer, { key: "e" });
      await act(async () => {
        vi.advanceTimersByTime(2000);
      });
    } finally {
      vi.useRealTimers();
    }
    expect(sockets).toHaveLength(2);
    await connectNewestTerminal();

    expect(screen.queryByText(/waiting for this folder/i)).toBeNull();
    expect(document.activeElement).toBe(composer);
  });

  it("does not take the cursor back once the user is entering text without keys", async () => {
    // Asked for the terminal from the composer, so focus never left the spot
    // the request remembers: only the user's activity can tell "still waiting"
    // from "writing again". Dictation, an IME commit or a context-menu paste
    // fires no keydown.
    const composer = renderApp();
    composer.focus();
    act(() => useTerminalStore.getState().toggleTerminal());
    await nextTask();
    enterTextWithoutKeys(composer, "hello there");

    settleNewWorkspace();
    await connectNewestTerminal();

    expect(document.activeElement).toBe(composer);
  });

  it("does not let another workspace's terminal claim a request made in this one", async () => {
    // Cmd+J in a workspace still being set up, while workspace B's terminal is
    // still the active session (A has none yet). Then the app switches to B
    // without a key or pointer — a notification click, a deep link, a
    // restore. B's terminal is already the active session, so it shows the
    // moment B does: the request was for A's terminal, not this one.
    const other = worktree({ id: "wt-other", name: "other", path: "/w/other", branch: "other", is_main: false });
    useWorktreeStore.setState({ worktrees: [mainWorktree, creatingWorktree, other], currentWorktree: creatingWorktree });
    useTerminalStore.setState({
      sessions: [sessionFor("wt-other", "t-other", "/w/other")],
      activeSessionId: "t-other",
      activeSessionPerWorktree: { "wt-other": "t-other" },
      isOpen: true,
    });
    const composer = renderApp();
    composer.focus();
    act(() => {
      window.dispatchEvent(new CustomEvent("focus-terminal"));
    });
    await nextTask();
    expect(useTerminalStore.getState().focusRequest).not.toBeNull();

    act(() => {
      useWorktreeStore.setState({ currentWorktree: other });
    });
    await connectNewestTerminal();

    expect(useTerminalStore.getState().focusRequest).toBeNull();
    expect(document.activeElement).toBe(composer);
  });

  it("keeps the cursor in the composer when switching to a workspace that has a terminal", async () => {
    const other = worktree({ id: "wt-other", name: "other", path: "/w/other", branch: "other", is_main: false });
    useWorktreeStore.setState({ worktrees: [mainWorktree, other], currentWorktree: mainWorktree });
    useTerminalStore.setState({
      sessions: [sessionFor("wt-main", "t-main", projectPath), sessionFor("wt-other", "t-other", "/w/other")],
      activeSessionId: "t-main",
      activeSessionPerWorktree: { "wt-main": "t-main", "wt-other": "t-other" },
      isOpen: true,
    });
    const composer = renderApp();
    composer.focus();

    act(() => {
      useWorktreeStore.setState({ currentWorktree: other });
    });

    expect(useTerminalStore.getState().activeSessionId).toBe("t-other");
    expect(document.activeElement).toBe(composer);
  });

  it("does not take the cursor back once the user has returned to typing elsewhere", async () => {
    const composer = renderApp();
    composer.focus();
    // Cmd+J while the workspace is still being set up...
    act(() => useTerminalStore.getState().toggleTerminal());
    await nextTask();
    // ...and, with nothing to type into yet, the user carries on in the chat.
    fireEvent.keyDown(composer, { key: "h" });

    settleNewWorkspace();
    await connectNewestTerminal();

    expect(document.activeElement).toBe(composer);
  });

  it("does not take the cursor back once the user has clicked elsewhere", async () => {
    const composer = renderApp();
    act(() => useTerminalStore.getState().toggleTerminal());
    await nextTask();
    fireEvent.pointerDown(composer);
    composer.focus();

    settleNewWorkspace();
    await connectNewestTerminal();

    expect(document.activeElement).toBe(composer);
  });
});

describe("terminal focus on explicit request", () => {
  beforeEach(() => {
    useWorktreeStore.setState({ worktrees: [mainWorktree], currentWorktree: mainWorktree });
    useTerminalStore.setState({
      sessions: [sessionFor("wt-main", "t-main", projectPath)],
      activeSessionId: "t-main",
      activeSessionPerWorktree: { "wt-main": "t-main" },
    });
  });

  it("puts the cursor in the terminal when the user opens the panel", async () => {
    const composer = renderApp();
    composer.focus();

    act(() => useTerminalStore.getState().toggleTerminal());

    expect(visibleTerminalHasFocus()).toBe(true);
  });

  it("puts the cursor in the terminal on the focus-terminal shortcut while the panel is open", async () => {
    useTerminalStore.setState({ isOpen: true });
    const composer = renderApp();
    composer.focus();
    expect(document.activeElement).toBe(composer);

    act(() => {
      window.dispatchEvent(new CustomEvent("focus-terminal"));
    });

    expect(visibleTerminalHasFocus()).toBe(true);
  });

  it("puts the cursor in a new terminal the user creates", async () => {
    useTerminalStore.setState({ isOpen: true });
    const composer = renderApp();
    composer.focus();

    act(() => {
      useTerminalStore.getState().createSession(projectPath, projectId, "wt-main");
    });

    expect(useTerminalStore.getState().activeSessionId).not.toBe("t-main");
    expect(visibleTerminalHasFocus()).toBe(true);
  });
});

describe("an explicit open on a workspace that is still being set up", () => {
  it("focuses the terminal once it exists if the user is still waiting for it", async () => {
    const composer = renderApp();
    composer.focus();
    act(() => useTerminalStore.getState().toggleTerminal());
    await nextTask();
    expect(useTerminalStore.getState().sessions).toHaveLength(0);

    settleNewWorkspace();

    expect(visibleTerminalHasFocus()).toBe(true);
  });

  it("lets activity inside the terminal panel keep the request alive", async () => {
    renderApp();
    act(() => useTerminalStore.getState().toggleTerminal());
    await nextTask();
    // The user clicks the panel's "waiting for the workspace" placeholder.
    fireEvent.pointerDown(screen.getByText(/waiting for the workspace/i));

    settleNewWorkspace();

    expect(visibleTerminalHasFocus()).toBe(true);
  });
});
