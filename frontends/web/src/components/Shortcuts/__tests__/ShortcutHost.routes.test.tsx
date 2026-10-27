/**
 * The keyboard host across the route tree.
 *
 * Mounted the way RootShell mounts it — around the outlet of a root route —
 * over the same layout ids routes.tsx uses (`_app` for the workspace,
 * `_authenticated` for Settings, the Inbox and the rest, `_mobile` under it),
 * with the real generated bindings, the real shortcuts store, the real
 * dispatcher and the real command palette. The workspace is a stub that
 * contributes handlers the way ModernApp does: one spy per workspace handler,
 * registered once it is ready.
 *
 * What it pins: shortcuts work off the workspace (palette, new chat, a
 * workspace-only shortcut), exactly one dispatcher lives while moving between
 * the workspace and Settings, every desktop menu id reaches its handler once,
 * and nothing is live where shortcuts do not belong (/auth, onboarding, the
 * phone surface, a signed-out guard).
 */

import { afterEach, beforeAll, beforeEach, describe, expect, it, vi } from "vitest";
import { act, fireEvent, render, screen, waitFor } from "@testing-library/react";
import { useState } from "react";
import {
  createMemoryHistory,
  createRootRoute,
  createRoute,
  createRouter,
  Outlet,
  RouterProvider,
} from "@tanstack/react-router";

const getShortcuts = vi.hoisted(() => vi.fn(async () => ({ shortcuts: "{}" })));

vi.mock("../../../api/client", async (importOriginal) => {
  const original = await importOriginal<Record<string, unknown>>();
  return {
    ...original,
    api: {
      ...(original.api as Record<string, unknown>),
      settings: {
        getShortcuts,
        updateShortcuts: vi.fn(async () => ({ message: "" })),
      },
    },
  };
});

// The chat search reads chats through React Query; its own tests cover it.
vi.mock("../../Layout/ChatSearch", () => ({
  ChatSearch: (props: { initialMode?: string }) => (
    <div data-testid="chat-search" data-mode={props.initialMode} />
  ),
}));

import { ShortcutHost, WORKSPACE_ACTION_TTL_MS } from "../ShortcutHost";
import {
  useShortcutLayer,
  useSuspendShortcuts,
  type ShortcutHandlers,
} from "../../../hooks/useShortcutHost";
import { useShortcutsStore } from "../../../store/shortcutsStore";
import { useAuthStore } from "../../../store/authStore";
import { useProjectStore, type Project } from "../../../store/projectStore";
import { useWorkspaceStateStore } from "../../../store/workspaceStateStore";
import { ShortcutRegistry } from "../../../lib/keyboard/registry";
import { OFF_WORKSPACE } from "../../../lib/keyboard/scope";

// ── Surface ────────────────────────────────────────────────────────────────

type Surface = { electron: boolean };

/** A Mac, so Cmd is metaKey; a browser tab unless `electron`. */
function setSurface({ electron }: Surface) {
  Object.defineProperty(window.navigator, "platform", {
    value: "MacIntel",
    configurable: true,
  });
  Object.defineProperty(window.navigator, "userAgent", {
    value: "Mozilla/5.0 (Macintosh; Intel Mac OS X 14_0)",
    configurable: true,
  });
  const w = window as unknown as Record<string, unknown>;
  w.RELIANT_CONFIG = electron ? { isElectron: true } : undefined;
}

/** The desktop app's menu bridge: main sends `menu-shortcut` with the id. */
function installElectronMenu() {
  const listeners = new Set<(shortcutId: string) => void>();
  const onMenuShortcut = vi.fn((callback: (shortcutId: string) => void) => {
    listeners.add(callback);
    return () => listeners.delete(callback);
  });
  // The find bar's opener (electron/src/find-bar.js).
  const openFindBar = vi.fn(async () => ({ success: true }));
  (window as unknown as Record<string, unknown>).electronAPI = {
    onMenuShortcut,
    updateShortcutBindings: vi.fn(async () => ({ success: true })),
    findInPage: { open: openFindBar },
  };
  return {
    onMenuShortcut,
    openFindBar,
    deliver: (shortcutId: string) =>
      act(() => listeners.forEach((listener) => listener(shortcutId))),
    subscribers: () => listeners.size,
  };
}

// ── The workspace, as ModernApp contributes it ─────────────────────────────

/** Every handler ModernApp owns: the ones the host does not implement. */
const WORKSPACE_HANDLERS = Object.entries(OFF_WORKSPACE)
  .filter(([, reach]) => reach !== "here" && reach !== "composer" && reach !== "builder")
  .map(([name]) => name);

let workspace: Record<string, ReturnType<typeof vi.fn>>;
let workspaceReady = true;
let setWorkspaceReady: (ready: boolean) => void = () => {};
/** A chat is open: the workspace shows a conversation, as ChatInterface marks it. */
let chatOpen = false;

function Workspace() {
  const [ready, setReady] = useState(workspaceReady);
  setWorkspaceReady = setReady;
  useShortcutLayer(
    { handlers: workspace as unknown as ShortcutHandlers, workspace: true },
    ready,
  );
  return (
    <div data-testid="workspace">
      workspace
      {chatOpen && <div data-context="chat-view" />}
    </div>
  );
}

function SettingsPage() {
  const [recording, setRecording] = useState(false);
  useSuspendShortcuts(recording);
  return (
    <div data-testid="settings">
      <input aria-label="Display name" />
      <button onClick={() => setRecording(true)}>Record shortcut</button>
    </div>
  );
}

// ── Router ─────────────────────────────────────────────────────────────────

function renderAt(path: string) {
  const root = createRootRoute({
    component: () => (
      <ShortcutHost>
        <Outlet />
      </ShortcutHost>
    ),
  });
  const page = (testId: string) => () => <div data-testid={testId}>{testId}</div>;

  const auth = createRoute({ getParentRoute: () => root, path: "/auth", component: page("auth") });
  const appLayout = createRoute({ getParentRoute: () => root, id: "_app", component: () => <Outlet /> });
  const index = createRoute({ getParentRoute: () => appLayout, path: "/", component: Workspace });
  const project = createRoute({
    getParentRoute: () => appLayout,
    path: "project/$projectId",
    component: Workspace,
  });
  const authed = createRoute({
    getParentRoute: () => root,
    id: "_authenticated",
    component: () => <Outlet />,
  });
  const settings = createRoute({ getParentRoute: () => authed, path: "/settings", component: SettingsPage });
  const inbox = createRoute({ getParentRoute: () => authed, path: "/inbox", component: page("inbox") });
  const workflows = createRoute({ getParentRoute: () => authed, path: "/workflows", component: page("workflows") });
  const onboarding = createRoute({
    getParentRoute: () => authed,
    path: "/onboarding",
    component: page("onboarding"),
  });
  const mobile = createRoute({ getParentRoute: () => authed, id: "_mobile", component: () => <Outlet /> });
  const mobileChats = createRoute({ getParentRoute: () => mobile, path: "/m/chats", component: page("mobile") });

  const router = createRouter({
    routeTree: root.addChildren([
      auth,
      appLayout.addChildren([index, project]),
      authed.addChildren([settings, inbox, workflows, onboarding, mobile.addChildren([mobileChats])]),
    ]),
    history: createMemoryHistory({ initialEntries: [path] }),
  });
  render(<RouterProvider router={router} />);
  return router;
}

/** Render and wait until the bindings have loaded. */
async function mountAt(path: string) {
  const router = renderAt(path);
  await waitFor(() =>
    expect(useShortcutsStore.getState().registry.size).toBeGreaterThan(0),
  );
  return router;
}

const pathOf = (router: ReturnType<typeof renderAt>) => router.state.location.pathname;

/** Press one chord at the focused element (or the body), as the browser would. */
function press(init: KeyboardEventInit & { key: string }, target: Element = document.activeElement ?? document.body) {
  const event = new KeyboardEvent("keydown", { bubbles: true, cancelable: true, ...init });
  act(() => {
    target.dispatchEvent(event);
  });
  return event;
}

const CMD_SHIFT_P = { key: "p", code: "KeyP", metaKey: true, shiftKey: true };
/** New chat in a browser tab (Cmd+T belongs to the browser). Option+N is a dead key. */
const NEW_CHAT_WEB = { key: "Dead", code: "KeyN", metaKey: true, altKey: true };
const CMD_J = { key: "j", code: "KeyJ", metaKey: true };
const CMD_F = { key: "f", code: "KeyF", metaKey: true };

const PROJECT: Project = {
  id: "p1",
  name: "my-project",
  path: "/home/me/my-project",
  is_git_repo: true,
  last_active: "2026-01-01T00:00:00Z",
  created_at: "2026-01-01T00:00:00Z",
  updated_at: "2026-01-01T00:00:00Z",
  is_forge: false,
};

/** Net keydown capture listeners on the document — the dispatcher's. */
let captureListeners = 0;

// The palette is code-split. The app warms its chunk as soon as the host goes
// live; here the first import is a module transform that can outlast a
// findBy timeout on a loaded machine, so pay it once up front.
beforeAll(async () => {
  await import("../../Layout/CommandPalette");
}, 60_000);

beforeEach(() => {
  // The palette scrolls its highlighted row into view; jsdom has no layout.
  Element.prototype.scrollIntoView = vi.fn();
  setSurface({ electron: false });
  workspaceReady = true;
  chatOpen = false;
  workspace = Object.fromEntries(WORKSPACE_HANDLERS.map((name) => [name, vi.fn()]));
  useShortcutsStore.setState({
    shortcuts: {},
    registry: new ShortcutRegistry([]),
    isLoading: false,
    _initialized: false,
  });
  getShortcuts.mockImplementation(async () => ({ shortcuts: "{}" }));
  useAuthStore.setState({ user: { id: "u1" } as never });
  useProjectStore.setState({ currentProject: PROJECT });
  // The chat workspace's URL carries the sidebar's project filter: filtered
  // to p1, "the workspace" is /project/p1.
  useWorkspaceStateStore.setState({ projectFilterId: "p1" });

  captureListeners = 0;
  const add = document.addEventListener.bind(document);
  const remove = document.removeEventListener.bind(document);
  vi.spyOn(document, "addEventListener").mockImplementation((type, listener, options) => {
    if (type === "keydown" && (options === true || (typeof options === "object" && options?.capture))) {
      captureListeners += 1;
    }
    add(type, listener, options);
  });
  vi.spyOn(document, "removeEventListener").mockImplementation((type, listener, options) => {
    if (type === "keydown" && (options === true || (typeof options === "object" && options?.capture))) {
      captureListeners -= 1;
    }
    remove(type, listener, options);
  });
});

afterEach(() => {
  const w = window as unknown as Record<string, unknown>;
  delete w.electronAPI;
  delete w.RELIANT_CONFIG;
  vi.useRealTimers();
  vi.restoreAllMocks();
  useAuthStore.setState({ user: null });
  useProjectStore.setState({ currentProject: null });
  useWorkspaceStateStore.setState({ projectFilterId: null });
  document.body.innerHTML = "";
});

// ── Off the workspace ──────────────────────────────────────────────────────

describe.each(["/settings", "/inbox"])("on %s", (path) => {
  it("opens the command palette with Cmd+Shift+P", async () => {
    const router = await mountAt(path);

    const event = press(CMD_SHIFT_P);

    expect(event.defaultPrevented).toBe(true);
    expect(await screen.findByPlaceholderText("Type a command...")).toBeInTheDocument();
    expect(pathOf(router)).toBe(path);
  });

  it("starts a new chat: into the workspace, then the workspace's own new chat, once", async () => {
    const router = await mountAt(path);

    press(NEW_CHAT_WEB);

    await waitFor(() => expect(pathOf(router)).toBe("/project/p1"));
    await waitFor(() => expect(workspace.onNewChat).toHaveBeenCalledOnce());
  });

  it("opens the terminal in the workspace on Cmd+J — revealed, not toggled", async () => {
    // Toggling a terminal that was already open would land on a workspace
    // with no terminal in it.
    const router = await mountAt(path);

    press(CMD_J);

    await waitFor(() => expect(pathOf(router)).toBe("/project/p1"));
    await waitFor(() => expect(workspace.onFocusTerminal).toHaveBeenCalledOnce());
    expect(workspace.onToggleTerminal).not.toHaveBeenCalled();
  });
});

describe("on /settings", () => {
  it("offers only what works from here in the palette", async () => {
    await mountAt("/settings");
    press(CMD_SHIFT_P);
    await screen.findByPlaceholderText("Type a command...");

    // Not the page you are on...
    expect(screen.queryByText("Open settings")).not.toBeInTheDocument();
    // ...but everywhere else, and workspace actions say where they happen.
    expect(screen.getByText("Inbox")).toBeInTheDocument();
    expect(screen.getByText("Open Terminal")).toBeInTheDocument();
    expect(screen.getByText("Show the terminal in the workspace")).toBeInTheDocument();
  });

  it("closes the palette on Escape, and stays on the page", async () => {
    const router = await mountAt("/settings");
    press(CMD_SHIFT_P);
    const input = await screen.findByPlaceholderText("Type a command...");

    press({ key: "Escape" }, input);

    await waitFor(() =>
      expect(screen.queryByPlaceholderText("Type a command...")).not.toBeInTheDocument(),
    );
    expect(pathOf(router)).toBe("/settings");
  });

  it("runs a palette command through the same handler as its shortcut", async () => {
    const router = await mountAt("/settings");
    press(CMD_SHIFT_P);
    await screen.findByPlaceholderText("Type a command...");

    fireEvent.click(screen.getByText("Open Terminal"));

    await waitFor(() => expect(pathOf(router)).toBe("/project/p1"));
    await waitFor(() => expect(workspace.onFocusTerminal).toHaveBeenCalledOnce());
  });

  it("leaves Cmd+F to the browser's find with no conversation on screen", async () => {
    await mountAt("/settings");

    const event = press(CMD_F);

    expect(event.defaultPrevented).toBe(false);
    expect(screen.queryByTestId("chat-search")).not.toBeInTheDocument();
  });

  it("leaves Escape and Cmd+Enter to the page", async () => {
    const router = await mountAt("/settings");
    const field = screen.getByLabelText("Display name");
    field.focus();

    const escape = press({ key: "Escape" }, field);
    const cmdEnter = press({ key: "Enter", metaKey: true }, field);

    expect(escape.defaultPrevented).toBe(false);
    expect(cmdEnter.defaultPrevented).toBe(false);
    expect(workspace.onStopStreaming).not.toHaveBeenCalled();
    expect(workspace.onApproveToolRequests).not.toHaveBeenCalled();
    expect(pathOf(router)).toBe("/settings");
  });

  it("leaves typing and text selection in a field alone", async () => {
    const router = await mountAt("/settings");
    const field = screen.getByLabelText("Display name");
    field.focus();

    // Cmd+Shift+Down is next chat everywhere else; in a field it selects to
    // the end.
    const select = press({ key: "ArrowDown", code: "ArrowDown", metaKey: true, shiftKey: true }, field);
    const typed = press({ key: "c", code: "KeyC" }, field);

    expect(select.defaultPrevented).toBe(false);
    expect(typed.defaultPrevented).toBe(false);
    expect(workspace.onNextChat).not.toHaveBeenCalled();
    expect(pathOf(router)).toBe("/settings");
  });

  it("closes the page on Close Tab, back to the workspace", async () => {
    const router = await mountAt("/settings");

    // Cmd+K X in a browser (Cmd+W belongs to it).
    press({ key: "k", code: "KeyK", metaKey: true });
    press({ key: "x", code: "KeyX" });

    await waitFor(() => expect(pathOf(router)).toBe("/project/p1"));
    // The workspace's own Close Tab would close a file the user never saw.
    await act(async () => {});
    expect(workspace.onCloseTab).not.toHaveBeenCalled();
  });

  it("stands down while a shortcut is being recorded", async () => {
    const menu = installElectronMenu();
    const router = await mountAt("/settings");
    fireEvent.click(screen.getByText("Record shortcut"));

    const event = press(CMD_SHIFT_P);
    menu.deliver("newChat");

    expect(event.defaultPrevented).toBe(false);
    expect(screen.queryByPlaceholderText("Type a command...")).not.toBeInTheDocument();
    await act(async () => {});
    expect(pathOf(router)).toBe("/settings");
  });

  it("drops a workspace action when the user goes elsewhere first", async () => {
    workspaceReady = false;
    const router = await mountAt("/settings");

    press(NEW_CHAT_WEB);
    await waitFor(() => expect(pathOf(router)).toBe("/project/p1"));
    await act(async () => {
      await router.navigate({ to: "/inbox" });
    });
    await act(async () => {
      await router.navigate({ to: "/project/$projectId", params: { projectId: "p1" } });
    });
    act(() => setWorkspaceReady(true));
    await act(async () => {});

    expect(workspace.onNewChat).not.toHaveBeenCalled();
  });

  it("drops a workspace action the workspace never got ready for", async () => {
    workspaceReady = false;
    const router = await mountAt("/settings");
    vi.useFakeTimers({ toFake: ["setTimeout", "clearTimeout"] });

    press(NEW_CHAT_WEB);
    await vi.waitFor(() => expect(pathOf(router)).toBe("/project/p1"));
    act(() => {
      vi.advanceTimersByTime(WORKSPACE_ACTION_TTL_MS + 1);
    });
    act(() => setWorkspaceReady(true));
    await act(async () => {});

    expect(workspace.onNewChat).not.toHaveBeenCalled();
  });
});

// ── In the workspace ───────────────────────────────────────────────────────

describe("in the workspace", () => {
  it("runs the workspace's handlers in place", async () => {
    const router = await mountAt("/project/p1");

    press(CMD_J);
    press(NEW_CHAT_WEB);

    expect(workspace.onToggleTerminal).toHaveBeenCalledOnce();
    expect(workspace.onNewChat).toHaveBeenCalledOnce();
    expect(workspace.onFocusTerminal).not.toHaveBeenCalled();
    expect(pathOf(router)).toBe("/project/p1");
  });

  it("waits for the workspace to be ready, then runs once", async () => {
    // ModernApp registers only after its workspace has restored; a shortcut
    // pressed during the restore must not act on state about to be replaced.
    workspaceReady = false;
    await mountAt("/project/p1");

    press(NEW_CHAT_WEB);
    expect(workspace.onNewChat).not.toHaveBeenCalled();

    act(() => setWorkspaceReady(true));
    await waitFor(() => expect(workspace.onNewChat).toHaveBeenCalledOnce());
  });

  it("searches the conversation on screen with Cmd+F", async () => {
    chatOpen = true;
    await mountAt("/project/p1");

    const event = press(CMD_F);

    expect(event.defaultPrevented).toBe(true);
    expect((await screen.findByTestId("chat-search")).dataset.mode).toBe("current");
  });

  it("leaves Cmd+F to the browser on a project with no chat open", async () => {
    await mountAt("/project/p1");

    const event = press(CMD_F);

    expect(event.defaultPrevented).toBe(false);
    expect(screen.queryByTestId("chat-search")).not.toBeInTheDocument();
  });

  it("always runs the newest closure, without re-registering", async () => {
    const seen: number[] = [];
    function Counter() {
      const [count, setCount] = useState(0);
      useShortcutLayer({
        handlers: { onToggleTerminal: () => seen.push(count) },
        workspace: true,
      });
      return <button onClick={() => setCount((c) => c + 1)}>bump</button>;
    }
    const root = createRootRoute({ component: () => <ShortcutHost><Outlet /></ShortcutHost> });
    const appLayout = createRoute({ getParentRoute: () => root, id: "_app", component: () => <Outlet /> });
    const index = createRoute({ getParentRoute: () => appLayout, path: "/", component: Counter });
    render(
      <RouterProvider
        router={createRouter({
          routeTree: root.addChildren([appLayout.addChildren([index])]),
          history: createMemoryHistory({ initialEntries: ["/"] }),
        })}
      />,
    );
    await waitFor(() => expect(useShortcutsStore.getState().registry.size).toBeGreaterThan(0));
    const listenersBefore = captureListeners;

    fireEvent.click(await screen.findByText("bump"));
    fireEvent.click(screen.getByText("bump"));
    press(CMD_J);

    expect(seen).toEqual([2]);
    expect(captureListeners).toBe(listenersBefore);
  });
});

// ── One dispatcher ─────────────────────────────────────────────────────────

describe("moving between the workspace and Settings", () => {
  it("keeps exactly one dispatcher, and fires each shortcut once", async () => {
    const router = await mountAt("/project/p1");
    expect(captureListeners).toBe(1);

    await act(async () => {
      await router.navigate({ to: "/settings" });
    });
    expect(captureListeners).toBe(1);
    await act(async () => {
      await router.navigate({ to: "/project/$projectId", params: { projectId: "p1" } });
    });
    expect(captureListeners).toBe(1);

    press(CMD_J);
    expect(workspace.onToggleTerminal).toHaveBeenCalledOnce();

    press(CMD_SHIFT_P);
    expect(await screen.findAllByPlaceholderText("Type a command...")).toHaveLength(1);
  });

  it("drops a Cmd+K left armed on the page the user navigated away from", async () => {
    const router = await mountAt("/project/p1");

    press({ key: "k", code: "KeyK", metaKey: true });
    await act(async () => {
      await router.navigate({ to: "/settings" });
    });
    const field = screen.getByLabelText("Display name");
    field.focus();
    const typed = press({ key: "c", code: "KeyC" }, field);

    // Not "Cmd+K C, focus the conversation" — just a "c" typed into Settings.
    expect(typed.defaultPrevented).toBe(false);
    await act(async () => {});
    expect(workspace.onFocusTranscript).not.toHaveBeenCalled();
    expect(pathOf(router)).toBe("/settings");
  });
});

// ── The desktop menu ───────────────────────────────────────────────────────

/** Every id the menu delivers, and the workspace handler it must reach. */
const MENU_SHORTCUTS: ReadonlyArray<readonly [id: string, handler: string]> = [
  ["newChat", "onNewChat"],
  ["closeTab", "onCloseTab"],
  ["reopenLastClosedFile", "onReopenLastClosedFile"],
  ["toggleTerminal", "onToggleTerminal"],
  ["newTerminal", "onNewTerminal"],
  ["clearTerminal", "onClearTerminal"],
  ["nextChat", "onNextChat"],
  ["prevChat", "onPrevChat"],
  ["nextRightSidebarTab", "onNextRightSidebarTab"],
  ["prevRightSidebarTab", "onPrevRightSidebarTab"],
];

describe("the desktop menu", () => {
  beforeEach(() => setSurface({ electron: true }));

  it.each(MENU_SHORTCUTS)("runs %s through %s, once", async (id, handler) => {
    const menu = installElectronMenu();
    await mountAt("/project/p1");

    menu.deliver(id);

    expect(workspace[handler]).toHaveBeenCalledOnce();
    for (const other of WORKSPACE_HANDLERS) {
      if (other !== handler) expect(workspace[other]).not.toHaveBeenCalled();
    }
  });

  it("runs Next Chat from Settings: into the workspace, then once", async () => {
    const menu = installElectronMenu();
    const router = await mountAt("/settings");

    menu.deliver("nextChat");

    await waitFor(() => expect(pathOf(router)).toBe("/project/p1"));
    await waitFor(() => expect(workspace.onNextChat).toHaveBeenCalledOnce());
  });

  it("clears the terminal from Settings, once the workspace is there to clear", async () => {
    const menu = installElectronMenu();
    const router = await mountAt("/inbox");

    menu.deliver("clearTerminal");

    await waitFor(() => expect(pathOf(router)).toBe("/project/p1"));
    await waitFor(() => expect(workspace.onClearTerminal).toHaveBeenCalledOnce());
  });

  it("subscribes once, and keeps the one subscription across routes", async () => {
    const menu = installElectronMenu();
    const router = await mountAt("/project/p1");
    expect(menu.subscribers()).toBe(1);

    await act(async () => {
      await router.navigate({ to: "/settings" });
    });
    expect(menu.subscribers()).toBe(1);
    expect(menu.onMenuShortcut).toHaveBeenCalledOnce();
  });
});

// ── Find in page ───────────────────────────────────────────────────────────

describe("Cmd+F in the desktop app", () => {
  beforeEach(() => setSurface({ electron: true }));

  it.each(["/settings", "/inbox", "/workflows", "/project/p1"])(
    "opens the find bar on %s, with no conversation on screen",
    async (path) => {
      const menu = installElectronMenu();
      document.documentElement.classList.add("dark");
      document.documentElement.setAttribute("data-color-scheme", "pure-black");
      await mountAt(path);

      const event = press(CMD_F);

      expect(event.defaultPrevented).toBe(true);
      expect(menu.openFindBar).toHaveBeenCalledOnce();
      expect(menu.openFindBar).toHaveBeenCalledWith({
        theme: { dark: true, colorScheme: "pure-black" },
      });
      expect(screen.queryByTestId("chat-search")).not.toBeInTheDocument();
      document.documentElement.classList.remove("dark");
      document.documentElement.removeAttribute("data-color-scheme");
    },
  );

  it("still searches the conversation on screen", async () => {
    const menu = installElectronMenu();
    chatOpen = true;
    await mountAt("/project/p1");

    press(CMD_F);

    expect((await screen.findByTestId("chat-search")).dataset.mode).toBe("current");
    expect(menu.openFindBar).not.toHaveBeenCalled();
  });

  it("still opens the editor's own find inside Monaco", async () => {
    const menu = installElectronMenu();
    await mountAt("/project/p1");
    const editor = document.createElement("div");
    editor.className = "monaco-editor";
    const textarea = document.createElement("textarea");
    editor.appendChild(textarea);
    document.body.appendChild(editor);
    textarea.focus();

    const event = press(CMD_F, textarea);

    // Passthrough: the editor's own keydown handler still sees it.
    expect(event.defaultPrevented).toBe(false);
    expect(menu.openFindBar).not.toHaveBeenCalled();
  });

  it("runs Edit › Find the way the key would: the find bar off a conversation", async () => {
    const menu = installElectronMenu();
    await mountAt("/settings");

    menu.deliver("findInPage");

    expect(menu.openFindBar).toHaveBeenCalledOnce();
  });

  it("runs Edit › Find the way the key would: the conversation search over one", async () => {
    const menu = installElectronMenu();
    chatOpen = true;
    await mountAt("/project/p1");

    menu.deliver("findInPage");

    expect((await screen.findByTestId("chat-search")).dataset.mode).toBe("current");
    expect(menu.openFindBar).not.toHaveBeenCalled();
  });

  it("drops the menu's echo of a Cmd+F it already handled", async () => {
    const menu = installElectronMenu();
    await mountAt("/inbox");

    press(CMD_F);
    menu.deliver("findInPage");

    expect(menu.openFindBar).toHaveBeenCalledOnce();
  });
});

describe("Cmd+F in a browser tab", () => {
  it("is the browser's own find, even remapped", async () => {
    // A remap made in the desktop app follows the user into the browser; a
    // desktop-only shortcut must not come with it.
    getShortcuts.mockImplementation(async () => ({
      shortcuts: JSON.stringify({ findInPage: { currentBinding: "Cmd+Shift+Y" } }),
    }));
    await mountAt("/settings");

    expect(press(CMD_F).defaultPrevented).toBe(false);
    expect(press({ key: "y", code: "KeyY", metaKey: true, shiftKey: true }).defaultPrevented).toBe(
      false,
    );
    expect(
      useShortcutsStore.getState().registry.get("findInPage")?.binding,
    ).toBe("");
  });
});

// ── Where there are no shortcuts ───────────────────────────────────────────

describe("no shortcuts", () => {
  it.each(["/auth", "/onboarding", "/m/chats"])("on %s", async (path) => {
    const menu = installElectronMenu();
    renderAt(path);
    await screen.findByTestId(path === "/auth" ? "auth" : path === "/onboarding" ? "onboarding" : "mobile");

    const event = press(CMD_SHIFT_P);

    expect(event.defaultPrevented).toBe(false);
    expect(captureListeners).toBe(0);
    expect(menu.onMenuShortcut).not.toHaveBeenCalled();
    expect(screen.queryByPlaceholderText("Type a command...")).not.toBeInTheDocument();
  });

  it("while the auth guard has no user", async () => {
    useAuthStore.setState({ user: null });
    renderAt("/settings");
    await screen.findByTestId("settings");

    const event = press(CMD_SHIFT_P);

    expect(event.defaultPrevented).toBe(false);
    expect(captureListeners).toBe(0);
  });

  it("and the dispatcher goes away on signing out", async () => {
    await mountAt("/settings");
    expect(captureListeners).toBe(1);

    act(() => useAuthStore.setState({ user: null }));

    expect(captureListeners).toBe(0);
  });
});

describe("the chat workspace's URL", () => {
  it("is / while the sidebar shows all projects", async () => {
    useWorkspaceStateStore.setState({ projectFilterId: null });
    const router = await mountAt("/settings");

    press(NEW_CHAT_WEB);

    await waitFor(() => expect(pathOf(router)).toBe("/"));
    await waitFor(() => expect(workspace.onNewChat).toHaveBeenCalledOnce());
  });
});
