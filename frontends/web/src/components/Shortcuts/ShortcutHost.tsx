/**
 * The keyboard host: the app's one keyboard dispatcher, menu bridge and
 * app-level shortcut surfaces, for every authenticated desktop route.
 *
 * It used to live inside ModernApp, which mounts only on `/` and
 * `/project/$projectId` — so on Settings, the Inbox, the Workflows area, Runs
 * and Forge no shortcut did anything, the command palette could not open, and
 * a desktop menu item that delivered a shortcut was dropped on the floor.
 *
 * Mounted once, in RootShell, around the whole route tree. Its context is
 * always provided; the dispatcher, the menu subscription and the overlays
 * mount only while `shortcutScope` says this route is an authenticated
 * desktop one, so there is never more than one listener and none on /auth,
 * /onboarding or the mobile surface. Moving between `/project/$id` and
 * `/settings` keeps the same dispatcher alive throughout.
 *
 * Handlers come from three places, looked up in this order:
 *   1. Layers contributed by mounted components (useShortcutLayer) — ModernApp
 *      once its workspace is ready, the workflow builder. Latest first.
 *   2. The app-level handlers below, which work on every route.
 *   3. For workspace handlers off the workspace, what lib/keyboard/scope says
 *      the shortcut does there: usually go into the workspace and run it once
 *      ModernApp is ready, or leave the page, or nothing — the key keeps its
 *      native meaning.
 */

import {
  Suspense,
  useCallback,
  useEffect,
  useLayoutEffect,
  useRef,
  useState,
  type ReactNode,
} from "react";
import { useNavigate, useRouterState } from "@tanstack/react-router";
import { useAuthStore } from "../../store/authStore";
import { useWorkspaceStateStore } from "../../store/workspaceStateStore";
import { openAddProject } from "../../store/addProjectDialogStore";
import { openProjectSwitcher } from "../../lib/projectSwitcher";
import {
  useAppKeyboardShortcuts,
  type ShortcutHandlerLookup,
} from "../../hooks/useAppKeyboardShortcuts";
import {
  ADD_SELECTION_TO_CHAT,
  ShortcutHostContext,
  type ShortcutHandlers,
  type ShortcutHost as ShortcutHostApi,
  type ShortcutLayer,
} from "../../hooks/useShortcutHost";
import { themeSnapshot } from "../../findBar/protocol";
import { chatOnScreen } from "../../lib/keyboard/contexts";
import { OFF_WORKSPACE } from "../../lib/keyboard/scope";
import { surfaceForPath } from "../../lib/surface";
import { lazyRoute } from "../../lib/lazyRoute";
import { logger } from "../../lib/logger";
import { SequenceHint } from "../Layout/SequenceHint";

// Loaded on first open (and warmed once the host is live): they would
// otherwise ride in the entry chunk to every unauthenticated page.
const CommandPalette = lazyRoute(
  () => import("../Layout/CommandPalette"),
  "CommandPalette",
);
const ChatSearch = lazyRoute(() => import("../Layout/ChatSearch"), "ChatSearch");

/** TanStack route ids of the two auth-guarded app layouts (routes.tsx). */
const WORKSPACE_LAYOUT = "/_app";
const APP_LAYOUT = "/_authenticated";
/**
 * Under `_authenticated`, but not the desktop app: onboarding is a setup flow
 * the workspace bounces back to, and `/m/*` is the phone surface.
 */
const EXCLUDED_ROUTES = new Set([
  "/_authenticated/onboarding",
  "/_authenticated/_mobile",
]);

/**
 * How long a workspace action requested from another page may wait for the
 * workspace to come up. A cold workspace restores its project, worktree and
 * chat first; an action still unrun after this is stale, not delayed.
 */
export const WORKSPACE_ACTION_TTL_MS = 15_000;

export interface ShortcutScope {
  /** Shortcuts run here: signed in, desktop surface, an app route. */
  enabled: boolean;
  /** This is the workspace (ModernApp's routes). */
  workspace: boolean;
}

/**
 * Whether the keyboard host runs on a route.
 *
 * Decided from the matched route ids rather than the pathname, so it follows
 * the route tree's own auth guards instead of a second list of paths, and
 * from the signed-in user, so a guard still resolving or redirecting to /auth
 * gets no shortcuts. Mobile is excluded by surface as well as by route.
 */
export function shortcutScope({
  routeIds,
  pathname,
  signedIn,
}: {
  routeIds: readonly string[];
  pathname: string;
  signedIn: boolean;
}): ShortcutScope {
  const enabled =
    signedIn &&
    surfaceForPath(pathname) === "desktop" &&
    routeIds.some((id) => id === WORKSPACE_LAYOUT || id === APP_LAYOUT) &&
    !routeIds.some((id) => EXCLUDED_ROUTES.has(id));
  return { enabled, workspace: enabled && routeIds.includes(WORKSPACE_LAYOUT) };
}

type Overlay =
  | { kind: "palette" }
  | { kind: "chat-search"; mode: "current" | "history" };

interface PendingWorkspaceAction {
  action: () => void;
  /** The page it was requested from. */
  origin: string;
  /** Whether the workspace route has been reached since. */
  landed: boolean;
  timer: ReturnType<typeof setTimeout>;
}

/** Everything the host's stable API reads about the current render. */
interface LiveState {
  enabled: boolean;
  workspace: boolean;
  pathname: string;
  navigate: ReturnType<typeof useNavigate>;
}

interface HostCore {
  host: ShortcutHostApi;
  lookup: ShortcutHandlerLookup;
  isAvailable: (handlerName: string) => boolean;
  isSuspended: () => boolean;
  /** Reconcile a queued workspace action with a route change. */
  onRouteChange: () => void;
  /** Drop a queued workspace action. */
  cancelPending: () => void;
  navigateToWorkspace: () => void;
}

function emit(name: string): void {
  window.dispatchEvent(new CustomEvent(name));
}

/** The desktop app's find bar is there to open (src/findBar/protocol.ts). */
function findInPageAvailable(): boolean {
  return typeof window.electronAPI?.findInPage?.open === "function";
}

/** A composer is mounted — the workspace's, a run's chat, the builder's. */
function composerOnScreen(): boolean {
  return document.querySelector('[data-context="chat-input"]') !== null;
}

function createHostCore(
  live: { readonly current: LiveState },
  setOverlay: (overlay: Overlay | null) => void,
): HostCore {
  const layers: Array<{ readonly current: ShortcutLayer }> = [];
  let pending: PendingWorkspaceAction | null = null;
  let suspensions = 0;

  const runSafely = (action: () => void, label: string) => {
    try {
      action();
    } catch (error) {
      logger.error(`Shortcut handler failed: ${label}`, error);
    }
  };

  /** The newest layer's handler, ignoring the host's own. */
  const layerHandler = (name: string) => {
    for (let i = layers.length - 1; i >= 0; i -= 1) {
      const handler = layers[i].current.handlers[name];
      if (handler) return handler;
    }
    return undefined;
  };

  const workspaceReady = () => layers.some((layer) => layer.current.workspace);

  const cancelPending = () => {
    if (!pending) return;
    clearTimeout(pending.timer);
    pending = null;
  };

  const flushPending = () => {
    const queued = pending;
    if (!queued || !workspaceReady()) return;
    cancelPending();
    runSafely(queued.action, "workspace action");
  };

  const navigateToWorkspace = () => {
    const { navigate } = live.current;
    // The chat workspace's URL carries the sidebar's project filter.
    const projectId = useWorkspaceStateStore.getState().projectFilterId;
    if (projectId) {
      void navigate({ to: "/project/$projectId", params: { projectId }, search: {} });
    } else {
      void navigate({ to: "/", search: {} });
    }
  };

  const runInWorkspace = (action: () => void) => {
    if (!live.current.enabled) return;
    if (workspaceReady()) {
      runSafely(action, "workspace action");
      return;
    }
    // The latest request wins; two queued shortcuts would run in an order the
    // user never saw.
    cancelPending();
    pending = {
      action,
      origin: live.current.pathname,
      landed: live.current.workspace,
      timer: setTimeout(cancelPending, WORKSPACE_ACTION_TTL_MS),
    };
    if (!live.current.workspace) navigateToWorkspace();
  };

  const closeOverlays = () => {
    setOverlay(null);
    for (const layer of layers) layer.current.closeOverlays?.();
  };

  const openOverlay = (overlay: Overlay) => {
    closeOverlays();
    setOverlay(overlay);
  };

  // Adding a project is a dialog; switching is the sidebar's project menu.
  // Both open over the chat workspace, which is where they act.
  const addProject = () => {
    closeOverlays();
    if (!live.current.workspace) navigateToWorkspace();
    openAddProject();
  };
  const switchProject = () => {
    closeOverlays();
    if (live.current.workspace) {
      openProjectSwitcher();
    } else {
      navigateToWorkspace();
      // The workspace mounts its sidebar on the next frame.
      requestAnimationFrame(() => openProjectSwitcher());
    }
  };

  /**
   * Act on the composer on screen; with none, go to the workspace and act on
   * its composer — through `inWorkspace` when the workspace has more to do.
   */
  const onComposer =
    (fire: () => void, inWorkspace: () => void = fire) =>
    () => {
      if (composerOnScreen()) fire();
      else runInWorkspace(inWorkspace);
    };

  /**
   * Cmd+F's "find on the page", and Edit › Find in the desktop menu. The key
   * reaches it only where neither a conversation nor the editor claims Cmd+F
   * first; the menu reaches it from anywhere, and over a conversation does
   * what the key does there — search the conversation.
   */
  const findInPage = () => {
    if (chatOnScreen()) {
      lookup("onChatSearch")?.();
      return;
    }
    closeOverlays();
    window.electronAPI.findInPage
      ?.open({ theme: themeSnapshot() })
      .catch((error: unknown) => logger.error("Failed to open the find bar", error));
  };

  const here: ShortcutHandlers = {
    onCommandPalette: () => openOverlay({ kind: "palette" }),
    // Cmd+F reaches this only over a conversation (its `chat-view` context);
    // the slash menu runs it from anywhere, and with no conversation on
    // screen there is nothing "current" to search, so it searches every chat.
    onChatSearch: () =>
      openOverlay({
        kind: "chat-search",
        mode: chatOnScreen() ? "current" : "history",
      }),
    // Desktop only: in a browser tab there is no handler — and no binding —
    // so the key reaches the browser's own find bar.
    onFindInPage: findInPageAvailable() ? findInPage : undefined,
    onSwitchChat: () => openOverlay({ kind: "chat-search", mode: "history" }),
    onToggleSettings: () => {
      if (live.current.pathname.startsWith("/settings")) navigateToWorkspace();
      else void live.current.navigate({ to: "/settings" });
    },
    onOpenWorkflows: () => void live.current.navigate({ to: "/workflows" }),
    onOpenProject: addProject,
    onSwitchProject: switchProject,
    // The file viewer whose editor has a selection claims the event and adds
    // it to the chat; with no taker the chord means what it does everywhere
    // else, focus the chat.
    onAddSelectionToChat: () => {
      const request = new CustomEvent(ADD_SELECTION_TO_CHAT, { cancelable: true });
      window.dispatchEvent(request);
      if (!request.defaultPrevented) lookup("onFocusChat")?.();
    },
    // Passthrough claims for editing contexts: the editor does the work.
    onEditorFind: () => {},
    onNativeSelection: () => {},
  };

  const composer: ShortcutHandlers = {
    onChangeModel: onComposer(() => emit("open-model-selector")),
    onChangeThinkingLevel: onComposer(() => emit("cycle-thinking-level")),
    onChangeWorkflow: onComposer(() => emit("open-workflow-selector")),
    onEditWorkflowParams: onComposer(() => emit("open-workflow-params")),
    // The composer owns the menu; focus it and ask it to open. Opening this
    // way does NOT insert a slash, so it works mid-sentence.
    onSlashCommands: onComposer(() => {
      emit("focus-chat-input");
      emit("open-slash-menu");
    }),
    onDismissSlashMenu: onComposer(() => emit("dismiss-slash-menu")),
    onComposerNewline: onComposer(() => emit("composer-insert-newline")),
    onFocusChat: onComposer(
      () => emit("focus-chat-input"),
      () => layerHandler("onFocusChat")?.(),
    ),
  };

  // Every handler's reach off the workspace, from the one table that decides
  // it. In the workspace, ModernApp's layer shadows the deferring ones.
  const base: ShortcutHandlers = {};
  for (const [name, reach] of Object.entries(OFF_WORKSPACE)) {
    if (reach === "here") base[name] = here[name];
    else if (reach === "composer") base[name] = composer[name];
    else if (reach === "workspace") {
      base[name] = () => runInWorkspace(() => layerHandler(name)?.());
    } else if (typeof reach === "object") {
      base[name] = () => runInWorkspace(() => layerHandler(reach.reveal)?.());
    } else if (reach === "leave") base[name] = navigateToWorkspace;
    // "native" and "builder": not registered here — the key keeps its own
    // meaning unless a mounted layer claims it.
  }

  const lookup: ShortcutHandlerLookup = (name) => layerHandler(name) ?? base[name];
  const isSuspended = () => suspensions > 0;

  const host: ShortcutHostApi = {
    register(layer) {
      layers.push(layer);
      // Run what was queued for the workspace once the current commit has
      // settled — the layer registers from a passive effect, after the panes
      // it renders have attached their listeners.
      if (layer.current.workspace && pending) queueMicrotask(flushPending);
      return () => {
        const index = layers.indexOf(layer);
        if (index !== -1) layers.splice(index, 1);
      };
    },
    run(handlerName) {
      if (!live.current.enabled || isSuspended()) return false;
      const handler = lookup(handlerName);
      if (!handler) return false;
      runSafely(handler, handlerName);
      return true;
    },
    runInWorkspace,
    closeOverlays,
    suspend() {
      suspensions += 1;
      let released = false;
      return () => {
        if (released) return;
        released = true;
        suspensions -= 1;
      };
    },
  };

  return {
    host,
    lookup,
    isAvailable: (name) => lookup(name) !== undefined,
    isSuspended,
    cancelPending,
    navigateToWorkspace,
    onRouteChange() {
      if (!pending) return;
      const { workspace, pathname } = live.current;
      if (workspace) {
        pending.landed = true;
        return;
      }
      // The user went somewhere else before the workspace came up — or came
      // and left again. Either way the request no longer describes what they
      // are doing.
      if (pending.landed || pathname !== pending.origin) cancelPending();
    },
  };
}

export function ShortcutHost({ children }: { children: ReactNode }) {
  // Both read from the RENDERED matches. Mid-navigation `location` already
  // names the destination while `matches` still describe the page on screen;
  // mixing the two made a transition into the workspace look like a detour
  // to some other page, which drops the action queued for the workspace.
  const pathname = useRouterState({
    select: (s) => s.matches[s.matches.length - 1]?.pathname ?? s.location.pathname,
  });
  // Joined so the selection compares by value and a re-render of the router
  // state with the same matches does not re-render the host.
  const routeIds = useRouterState({
    select: (s) => s.matches.map((match) => match.routeId).join(" "),
  });
  const signedIn = useAuthStore((state) => state.user !== null);
  const { enabled, workspace } = shortcutScope({
    routeIds: routeIds.split(" "),
    pathname,
    signedIn,
  });

  const navigate = useNavigate();
  const [overlay, setOverlay] = useState<Overlay | null>(null);

  const live = useRef<LiveState>({
    enabled,
    workspace,
    pathname,
    navigate,
  });
  useLayoutEffect(() => {
    live.current = { enabled, workspace, pathname, navigate };
  });

  const [core] = useState(() => createHostCore(live, setOverlay));

  useEffect(() => {
    core.onRouteChange();
  }, [core, pathname, workspace]);

  useEffect(() => {
    if (enabled) return;
    core.cancelPending();
    setOverlay(null);
  }, [core, enabled]);

  const closeOverlay = useCallback(() => setOverlay(null), []);

  return (
    <ShortcutHostContext.Provider value={core.host}>
      {children}
      {enabled && (
        <ActiveShortcuts
          core={core}
          pathname={pathname}
          workspace={workspace}
          overlay={overlay}
          onCloseOverlay={closeOverlay}
        />
      )}
    </ShortcutHostContext.Provider>
  );
}

/** The live half: mounted only where shortcuts run. */
function ActiveShortcuts({
  core,
  pathname,
  workspace,
  overlay,
  onCloseOverlay,
}: {
  core: HostCore;
  pathname: string;
  workspace: boolean;
  overlay: Overlay | null;
  onCloseOverlay: () => void;
}) {
  const { pendingSequence, clearPendingSequence } = useAppKeyboardShortcuts(
    core.lookup,
    { isSuspended: core.isSuspended },
  );

  // A route change is unrelated input: a Cmd+K armed on one page must not
  // complete on the next one as whatever that key means there.
  useEffect(() => {
    clearPendingSequence();
  }, [pathname, clearPendingSequence]);

  // Warm the overlays so the first Cmd+Shift+P does not wait on a fetch.
  useEffect(() => {
    void CommandPalette.preload?.();
    void ChatSearch.preload?.();
  }, []);

  return (
    <>
      <SequenceHint pending={pendingSequence} isAvailable={core.isAvailable} />
      <Suspense fallback={null}>
        {overlay?.kind === "palette" && (
          <CommandPalette isOpen inWorkspace={workspace} onClose={onCloseOverlay} />
        )}
        {overlay?.kind === "chat-search" && (
          <ChatSearch
            // A different tab is a different search: remount rather than
            // carry the last query across.
            key={overlay.mode}
            isOpen
            initialMode={overlay.mode}
            onClose={onCloseOverlay}
            onChatOpened={() => {
              if (!workspace) core.navigateToWorkspace();
            }}
          />
        )}
      </Suspense>
    </>
  );
}
