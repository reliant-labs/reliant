import React, { createContext, useContext, useEffect, useLayoutEffect, useRef } from "react";

/**
 * useModalDialog — the lifecycle every modal surface shares: the stack of open
 * dialogs (Escape and the focus trap belong to the top one), focus in and back,
 * the Tab cycle, background isolation and the counted scroll lock. `<Modal>`
 * (./modal) is built on it; a custom dialog surface (a drawer, a sheet) calls
 * it with its own panel ref to join the same stack.
 *
 * It is its own module, not an export of modal.tsx, because a file that
 * exports components AND a hook defeats React Fast Refresh: an edit to it
 * reloads the page instead of hot-swapping the components
 * (eslint-plugin-react-refresh's only-export-components).
 */

export const FOCUSABLE_SELECTOR =
  'a[href], button:not([disabled]), textarea:not([disabled]), input:not([disabled]), select:not([disabled]), [tabindex]:not([tabindex="-1"])';

// Whether the reader can Tab to (or be put back on) el. A control's own
// computed display says nothing of its ancestors': a desktop-only button in a
// `max-md:hidden` wrapper is display:block itself, and counting it as the
// cycle's last stop is how Tab from the real last stop escaped the dialog.
// Visibility inherits, so the element's own value already reflects its
// ancestors (and a visible child of a hidden parent is reachable).
function reachable(el: HTMLElement): boolean {
  if (
    el.matches(":disabled") ||
    el.closest('[inert], [hidden], [aria-hidden="true"]') ||
    getComputedStyle(el).visibility === "hidden"
  )
    return false;
  for (
    let node: HTMLElement | null = el;
    node && node !== document.body;
    node = node.parentElement
  ) {
    if (getComputedStyle(node).display === "none") return false;
  }
  return true;
}

// Open dialogs, bottom to top. Escape and the focus trap belong to the top
// one only; a module-level stack because stacked dialogs are usually
// rendered by unrelated components.
interface DialogEntry {
  id: symbol;
  panel: HTMLElement;
  ref: React.RefObject<HTMLElement | null>;
  ancestors: React.RefObject<HTMLElement | null>[];
  containFocus: () => void;
  external: HTMLElement | null;
}
/** The dialogs a surface is nested in; `<Modal>` provides it to its children. */
export const DialogAncestors = createContext<React.RefObject<HTMLElement | null>[]>([]);
const openStack: DialogEntry[] = [];
// React removes portal DOM after layout cleanup; a closing registered panel
// must not be rediscovered as a legacy external overlay in that interval.
const registeredPanels = new WeakSet<HTMLElement>();
// React layout effects run children first. A nested Modal is above its parent
// even when the parent's effect registers later in that same commit.
function registerDialog(entry: DialogEntry) {
  registeredPanels.add(entry.panel);
  const child = openStack.findIndex((other) => other.ancestors.includes(entry.ref));
  if (child < 0) openStack.push(entry);
  else openStack.splice(child, 0, entry);
}
const isTop = (id: symbol) => openStack[openStack.length - 1]?.id === id;

// External dialogs predate this hook. Cooperate with a visible modal layer
// painted above ours, not every aria-modal node anywhere on the page. Compare
// the first divergent stacking contexts; equal z-index uses DOM paint order.
function stackingPath(el: HTMLElement): HTMLElement[] {
  const path: HTMLElement[] = [];
  for (
    let node: HTMLElement | null = el;
    node && node !== document.body;
    node = node.parentElement
  ) {
    const style = getComputedStyle(node);
    if (
      style.position === "fixed" ||
      style.position === "sticky" ||
      (style.zIndex !== "" && style.zIndex !== "auto") ||
      (style.transform !== "" && style.transform !== "none") ||
      style.isolation === "isolate"
    )
      path.unshift(node);
  }
  if (path[path.length - 1] !== el) path.push(el);
  return path;
}
function paintedAbove(candidate: HTMLElement, panel: HTMLElement): boolean {
  if (panel.contains(candidate)) return true;
  if (candidate.contains(panel)) return false;
  const a = stackingPath(candidate);
  const b = stackingPath(panel);
  let i = 0;
  while (a[i] && a[i] === b[i]) i++;
  const left = a[i] ?? candidate;
  const right = b[i] ?? panel;
  const z = (el: HTMLElement) => parseInt(getComputedStyle(el).zIndex, 10) || 0;
  if (z(left) !== z(right)) return z(left) > z(right);
  return !!(right.compareDocumentPosition(left) & Node.DOCUMENT_POSITION_FOLLOWING);
}
function externalAbove(panel: HTMLElement): HTMLElement | null {
  let top: HTMLElement = panel;
  for (const candidate of document.querySelectorAll<HTMLElement>(
    '[role="dialog"][aria-modal="true"], [role="alertdialog"][aria-modal="true"]',
  )) {
    if (registeredPanels.has(candidate)) continue;
    // Ignore our own temporary isolation, but respect application-owned hidden
    // or inert state and hidden ancestors.
    let visible = true;
    for (
      let node: HTMLElement | null = candidate;
      node && node !== document.body;
      node = node.parentElement
    ) {
      const saved = isolated.get(node);
      const style = getComputedStyle(node);
      if (
        node.hidden ||
        (saved ? saved.inert !== null : node.hasAttribute("inert")) ||
        (saved ? saved.hidden === "true" : node.getAttribute("aria-hidden") === "true") ||
        style.display === "none" ||
        style.visibility === "hidden"
      )
        visible = false;
    }
    if (visible && paintedAbove(candidate, top)) top = candidate;
  }
  return top === panel ? null : top;
}

// A menu/picker can portal outside the dialog DOM. aria-controls ties it to
// its trigger without importing that component or mistaking unrelated portals
// for part of this modal. Follow chains (a picker inside a portaled menu).
function modalSurfaces(panel: HTMLElement): HTMLElement[] {
  const surfaces = [panel];
  for (const surface of surfaces) {
    for (const control of surface.querySelectorAll("[aria-controls]")) {
      for (const id of (control.getAttribute("aria-controls") ?? "").split(/\s+/)) {
        const owned = document.getElementById(id);
        if (owned && !surfaces.some((surface) => surface.contains(owned))) surfaces.push(owned);
      }
    }
  }
  return surfaces;
}

// Restore before re-isolating: a new portal must never inherit an older
// dialog's inert background. Keep exact attributes, including existing inert.
const isolated = new Map<Element, { inert: string | null; hidden: string | null }>();
function restoreBackground() {
  for (const [el, saved] of isolated) {
    if (saved.inert === null) el.removeAttribute("inert");
    else el.setAttribute("inert", saved.inert);
    if (saved.hidden === null) el.removeAttribute("aria-hidden");
    else el.setAttribute("aria-hidden", saved.hidden);
  }
  isolated.clear();
}
function isolateBackground() {
  restoreBackground();
  const panel = openStack[openStack.length - 1]?.panel;
  if (!panel) return;
  const external = externalAbove(panel);
  const owner = openStack[openStack.length - 1];
  if (owner && external) owner.external = external;
  const surfaces = modalSurfaces(external ?? panel).map(
    (el) => el.closest("[data-modal-layer]") ?? el,
  );
  const visit = (parent: Element) => {
    for (const child of Array.from(parent.children)) {
      if (child.matches("script, style, link") || surfaces.includes(child)) continue;
      if (surfaces.some((surface) => child.contains(surface))) {
        visit(child);
        continue;
      }
      isolated.set(child, {
        inert: child.getAttribute("inert"),
        hidden: child.getAttribute("aria-hidden"),
      });
      child.setAttribute("inert", "");
      child.setAttribute("aria-hidden", "true");
    }
  };
  visit(document.body);
  // Removing a focused node need not emit focusin (browsers can leave body
  // active). Reconcile focus after DOM mutations too, not only on Tab.
  if (external) {
    // The external surface retains its own Tab/Escape policy. Only keep page
    // focus from falling behind it during lower teardown or portal removal.
    if (!modalSurfaces(external).some((surface) => surface.contains(document.activeElement))) {
      (external.querySelector<HTMLElement>(FOCUSABLE_SELECTOR) ?? external).focus({
        preventScroll: true,
      });
    }
  } else openStack[openStack.length - 1]?.containFocus();
}

// Shared by Modal and SidebarLayout, so closing either cannot unlock the other.
let lockCount = 0;
let pendingExternal: HTMLElement | null = null;
let pendingUnlock: MutationObserver | null = null;
let savedStyles: { name: string; value: string; priority: string }[] = [];
function lockScroll() {
  if (lockCount++ === 0 && !pendingUnlock) {
    savedStyles = ["overscroll-behavior", "padding-right", "overflow"].map((name) => ({
      name,
      value: document.body.style.getPropertyValue(name),
      priority: document.body.style.getPropertyPriority(name),
    }));
    const width = document.documentElement.clientWidth;
    const scrollbar = width > 0 ? window.innerWidth - width : 0;
    if (scrollbar > 0) {
      document.body.style.paddingRight = `${(parseFloat(getComputedStyle(document.body).paddingRight) || 0) + scrollbar}px`;
    }
    document.body.style.setProperty("overscroll-behavior", "none");
    document.body.style.setProperty("overflow", "hidden");
  }
}
function restoreScroll() {
  for (const { name, value, priority } of savedStyles) {
    if (value) document.body.style.setProperty(name, value, priority);
    else document.body.style.removeProperty(name);
  }
}
function externalLockActive(panel: HTMLElement | null): boolean {
  if (
    !panel?.isConnected ||
    !panel.matches('[role="dialog"][aria-modal="true"], [role="alertdialog"][aria-modal="true"]')
  )
    return false;
  for (
    let node: HTMLElement | null = panel;
    node && node !== document.body;
    node = node.parentElement
  ) {
    const saved = isolated.get(node);
    const style = getComputedStyle(node);
    if (
      node.hidden ||
      (saved ? saved.inert !== null : node.hasAttribute("inert")) ||
      (saved ? saved.hidden === "true" : node.getAttribute("aria-hidden") === "true") ||
      style.display === "none" ||
      style.visibility === "hidden"
    )
      return false;
  }
  return true;
}
function retainExternalLock(external: HTMLElement) {
  if (pendingExternal === external && pendingUnlock) return;
  pendingUnlock?.disconnect();
  pendingExternal = external;
  // This is a cooperative lease on a *visible modal*, not on a DOM node's
  // connectivity. Legacy overlays may close in place (hidden, inert, CSS or
  // aria-modal=false); a no-op callback while still visible does not close it.
  pendingUnlock = new MutationObserver(() => {
    if (externalLockActive(pendingExternal)) return;
    pendingUnlock?.disconnect();
    pendingUnlock = null;
    pendingExternal = null;
    // MutationObserver receives removal itself, after synchronous React
    // cleanup. No extra mutation or arbitrary timer is needed to settle it.
    if (lockCount === 0) restoreScroll();
    else {
      // A reopened Forge dialog still owns the lock. A legacy cleanup may
      // just have restored a stale value, so reassert only our two properties.
      document.body.style.setProperty("overscroll-behavior", "none");
      document.body.style.setProperty("overflow", "hidden");
    }
  });
  pendingUnlock.observe(document.body, {
    childList: true,
    subtree: true,
    attributes: true,
    attributeFilter: ["hidden", "inert", "aria-hidden", "aria-modal", "role", "class", "style"],
  });
}
function unlockScroll(external: HTMLElement | null) {
  // Carry the final page snapshot until both the counted Forge owners and
  // the cooperative external owner end. Reopening never takes a new snapshot
  // of "hidden" or creates a second observer for the same external layer.
  if (external && externalLockActive(external)) retainExternalLock(external);
  if (--lockCount !== 0) return;
  if (pendingUnlock && externalLockActive(pendingExternal)) return;
  pendingUnlock?.disconnect();
  pendingUnlock = null;
  pendingExternal = null;
  restoreScroll();
}

/** Shared modal lifecycle; use the same stack for custom dialog surfaces. */
export function useModalDialog({
  open,
  panelRef,
  onClose,
  initialFocus,
  returnFocus,
  fallbackFocus,
}: {
  open: boolean;
  panelRef: React.RefObject<HTMLElement | null>;
  onClose: () => void;
  initialFocus?: () => HTMLElement | null | undefined;
  returnFocus?: () => HTMLElement | null;
  fallbackFocus?: () => void;
}) {
  const ancestors = useContext(DialogAncestors);
  const callbacks = useRef({ onClose, initialFocus, returnFocus, fallbackFocus });
  // Read when the dialog registers, like callbacks: a nesting change alone
  // must not re-register (and so re-focus) an open dialog.
  const ancestorsRef = useRef(ancestors);
  const passiveScrollSettlement = useRef<(() => void) | null>(null);
  // Parent passive cleanup can precede a legacy child's passive cleanup.
  // A microtask from this phase runs after both, unlike layout cleanup (which
  // may run after the external portal DOM has already disappeared).
  useEffect(() => {
    if (!open) passiveScrollSettlement.current?.();
    return () => passiveScrollSettlement.current?.();
  }, [open]);
  useLayoutEffect(() => {
    callbacks.current = { onClose, initialFocus, returnFocus, fallbackFocus };
    ancestorsRef.current = ancestors;
  });
  useLayoutEffect(() => {
    const panel = panelRef.current;
    if (!open || !panel) return;
    const id = Symbol("modal");
    const active = document.activeElement;
    const previouslyFocused =
      callbacks.current.returnFocus?.() ??
      (active instanceof HTMLElement && active !== document.body ? active : null);
    let lastFocused: HTMLElement | null = null;
    const containsFocus = () =>
      modalSurfaces(panel).some((surface) => surface.contains(document.activeElement));
    const containFocus = () => {
      if (!isTop(id) || !panel.isConnected) return;
      if (externalAbove(panel)) {
        isolateBackground();
        return;
      }
      if (containsFocus()) return;
      const target =
        lastFocused?.isConnected &&
        modalSurfaces(panel).some((surface) => surface.contains(lastFocused)) &&
        reachable(lastFocused)
          ? lastFocused
          : (callbacks.current.initialFocus?.() ?? focusable()[0] ?? panel);
      target.focus({ preventScroll: true });
    };
    const entry: DialogEntry = {
      id,
      panel,
      ref: panelRef,
      ancestors: ancestorsRef.current,
      containFocus,
      external: null,
    };
    registerDialog(entry);
    lockScroll();
    // The panel is where focus rests when it has nothing to Tab to (and the
    // fallback whenever focus must be put back): it has to be focusable. A
    // panel that declares its own tabIndex keeps it; one the hook made
    // focusable is put back as it was on close.
    const madeFocusable = !panel.hasAttribute("tabindex");
    if (madeFocusable) panel.tabIndex = -1;
    // Release the lower dialog's isolation before focusing a new portal.
    // Its new background is isolated only AFTER focus has moved out of it.
    restoreBackground();

    const focusable = () =>
      modalSurfaces(panel)
        .flatMap((surface) => [
          ...(surface !== panel && surface.matches(FOCUSABLE_SELECTOR) ? [surface] : []),
          ...Array.from(surface.querySelectorAll<HTMLElement>(FOCUSABLE_SELECTOR)),
        ])
        .filter((el) => el.tabIndex >= 0 && reachable(el));
    if (isTop(id) && !externalAbove(panel) && !panel.contains(document.activeElement)) {
      (callbacks.current.initialFocus?.() ?? focusable()[0] ?? panel).focus();
    }
    if (document.activeElement instanceof HTMLElement && containsFocus()) {
      lastFocused = document.activeElement;
    }
    const onFocusIn = () => {
      if (!isTop(id)) return;
      if (externalAbove(panel)) {
        isolateBackground();
        return;
      }
      if (containsFocus() && document.activeElement instanceof HTMLElement) {
        lastFocused = document.activeElement;
      } else containFocus();
    };
    document.addEventListener("focusin", onFocusIn);
    isolateBackground();
    // Portals created while open also belong to the background unless they
    // register a new modal above us. Observe child changes, not our attributes.
    const observer = new MutationObserver(isolateBackground);
    observer.observe(document.body, { childList: true, subtree: true });
    const onKeyDown = (e: KeyboardEvent) => {
      if (!isTop(id) || externalAbove(panel)) return;
      if (e.key === "Escape") {
        if (e.defaultPrevented) return;
        e.preventDefault();
        callbacks.current.onClose();
        return;
      }
      if (e.key !== "Tab") return;
      // Resolved on every keypress: content added, removed or hidden after
      // opening moves the ends of the cycle.
      const items = focusable();
      const first = items[0];
      const last = items[items.length - 1];
      const active = document.activeElement;
      const owner = active instanceof Element ? active.closest('[aria-modal="true"]') : null;
      if (owner && owner !== panel && externalAbove(panel) === owner) return;
      const inside = modalSurfaces(panel).some((surface) => surface.contains(active));
      // An empty panel keeps focus. Otherwise wrap at both ends; from the
      // panel itself (or from outside it) Tab enters at the matching end.
      if (!first) {
        e.preventDefault();
        panel.focus();
      } else if (e.shiftKey && (!inside || active === first || active === panel)) {
        e.preventDefault();
        (last ?? first).focus();
      } else if (!e.shiftKey && (!inside || active === last || active === panel)) {
        e.preventDefault();
        first.focus();
      }
    };
    document.addEventListener("keydown", onKeyDown);
    return () => {
      observer.disconnect();
      document.removeEventListener("keydown", onKeyDown);
      document.removeEventListener("focusin", onFocusIn);
      if (madeFocusable) panel.removeAttribute("tabindex");
      const wasTop = isTop(id);
      const external = wasTop ? (externalAbove(panel) ?? entry.external) : null;
      if (external) {
        const snapshot = savedStyles;
        passiveScrollSettlement.current = () => {
          passiveScrollSettlement.current = null;
          queueMicrotask(() => {
            // Never restore over a reopened Forge or still-visible legacy
            // owner, nor over a later generation's page-style snapshot.
            if (
              savedStyles === snapshot &&
              lockCount === 0 &&
              !externalLockActive(external) &&
              !externalLockActive(pendingExternal)
            ) {
              pendingUnlock?.disconnect();
              pendingUnlock = null;
              pendingExternal = null;
              restoreScroll();
            }
          });
        };
      }
      openStack.splice(
        openStack.findIndex((entry) => entry.id === id),
        1,
      );
      isolateBackground();
      unlockScroll(external);
      if (!wasTop || external) return;
      if (
        previouslyFocused?.isConnected &&
        !previouslyFocused.closest("[inert], [hidden]") &&
        !previouslyFocused.matches(":disabled") &&
        getComputedStyle(previouslyFocused).display !== "none" &&
        getComputedStyle(previouslyFocused).visibility !== "hidden"
      ) {
        previouslyFocused.focus();
      } else callbacks.current.fallbackFocus?.();
    };
  }, [open, panelRef]);
}
