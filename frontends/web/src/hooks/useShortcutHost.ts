/**
 * The keyboard host's API, for the components that contribute to it.
 *
 * There is exactly one keyboard dispatcher in the app, owned by ShortcutHost
 * (components/Shortcuts/ShortcutHost.tsx) at the root of the route tree. It
 * implements the app-level shortcuts itself; a component that owns more of
 * them — ModernApp's workspace, the workflow builder — contributes its
 * handlers as a LAYER for as long as it is mounted, rather than mounting a
 * dispatcher of its own. Two dispatchers would both see every keystroke.
 *
 * Kept apart from the host component so contributors do not import the host's
 * overlays (and the host can import components that use this).
 */

import {
  createContext,
  useContext,
  useEffect,
  useLayoutEffect,
  useRef,
  type RefObject,
} from "react";

export type ShortcutHandlers = Record<string, (() => void) | undefined>;

export interface ShortcutLayer {
  /** Handlers by name (config/shortcuts.yaml `handler`). */
  handlers: ShortcutHandlers;
  /**
   * The workspace (ModernApp), registered once it is ready to act — project
   * selected or picked, workspace restored. Actions requested from other
   * routes wait for this, then run on it.
   */
  workspace?: boolean;
  /** Close the overlays this layer owns, when another one opens. */
  closeOverlays?: () => void;
}

export interface ShortcutHost {
  /**
   * Contribute a layer. Read through the ref on every lookup, so the latest
   * closures always run; returns the unregister function.
   */
  register(layer: RefObject<ShortcutLayer>): () => void;
  /**
   * Run a shortcut's action by handler name, exactly as its keystroke would —
   * for surfaces that offer the same actions (the slash menu, the palette,
   * sidebar buttons). False when nothing ran.
   */
  run(handlerName: string): boolean;
  /**
   * Run an action in the workspace: now, if it is up; otherwise after going
   * there and waiting for it to be ready.
   */
  runInWorkspace(action: () => void): void;
  /** Close every overlay the host and its layers own. */
  closeOverlays(): void;
  /**
   * Stand keystrokes and menu deliveries down — the remap capture box needs
   * the raw chord. Returns the release.
   */
  suspend(): () => void;
}

export const ShortcutHostContext = createContext<ShortcutHost | null>(null);

/**
 * The addSelectionToChat shortcut's request, a cancelable window event. The
 * file viewer whose focused editor has a selection takes it (preventDefault)
 * and adds that selection to the chat; untaken, the shortcut focuses the chat.
 */
export const ADD_SELECTION_TO_CHAT = "add-selection-to-chat";

/** The app's keyboard host, or null outside it (tests, isolated previews). */
export function useShortcutHost(): ShortcutHost | null {
  return useContext(ShortcutHostContext);
}

/**
 * Contribute shortcut handlers while this component is mounted (and `enabled`).
 *
 * Handlers may change identity every render; the layer is updated in place,
 * so the dispatcher never re-registers and always calls the latest closure.
 * Unmounting removes it, so no handler outlives its component.
 */
export function useShortcutLayer(layer: ShortcutLayer, enabled = true): void {
  const host = useShortcutHost();
  const layerRef = useRef(layer);

  // Assigned after commit rather than during render: a render React throws
  // away must not leave its closures behind for the next keystroke.
  useLayoutEffect(() => {
    layerRef.current = layer;
  });

  // A passive effect on purpose. React runs them child-first, so by the time
  // ModernApp's layer registers — and the host runs any action queued for the
  // workspace — the composer, terminal and panes it renders have attached
  // their own listeners.
  useEffect(() => {
    if (!host || !enabled) return;
    return host.register(layerRef);
  }, [host, enabled]);
}

/** Suspend every shortcut while `active` (see ShortcutHost.suspend). */
export function useSuspendShortcuts(active: boolean): void {
  const host = useShortcutHost();
  useEffect(() => {
    if (!host || !active) return;
    return host.suspend();
  }, [host, active]);
}
