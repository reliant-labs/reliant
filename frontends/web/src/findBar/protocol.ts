/**
 * The desktop find bar's contract: what the app sends to open it, and what
 * the bar page and the Electron main process say to each other.
 *
 * The bar is NOT part of the page it searches. Electron's
 * `webContents.findInPage` matches text inside the page's own inputs, so a
 * search box rendered in the page counts — and lands on — its own query, and
 * every search moves focus out of it to the active match. Chrome's answer is
 * to keep the find bar out of the page, and so does the desktop app: the bar
 * is a small separate view (electron/src/find-bar.js) loading find-bar.html,
 * whose React root is FindBar. Searching the window's page from there leaves
 * the bar's focus, caret and text alone.
 *
 * The channel names live with the main process (electron/src/find-bar.js);
 * both preloads repeat them, and electron's find-bar test holds them equal.
 */

/** The app's look at the moment it opened the bar, applied to the bar page. */
export interface FindBarTheme {
  /** The `.dark` class on the app's root element. */
  dark: boolean;
  /** The app's `data-color-scheme`, or null when it has none. */
  colorScheme: string | null;
}

/** The app → main: show the bar over this window and focus it. */
export interface FindBarOpenRequest {
  theme: FindBarTheme;
}

/** Main → the bar page, each time the bar is shown. */
export interface FindBarOpened {
  theme: FindBarTheme;
}

/**
 * The bar page → main: search the window's page. Mirrors Electron's
 * `findInPage` options; `findNext` starts a new search session (a changed
 * query) and is false for stepping through the current one.
 */
export interface FindRequest {
  text: string;
  forward: boolean;
  findNext: boolean;
  matchCase: boolean;
}

/** Main → the bar page: where the latest search stands. */
export interface FindResult {
  matches: number;
  /** 1-based; 0 when there are no matches. */
  activeMatchOrdinal: number;
  finalUpdate: boolean;
}

/** What find-bar-preload.js exposes to the bar page as `window.findBarAPI`. */
export interface FindBarAPI {
  /** The page is listening: main may now tell it the bar is open. */
  ready: () => void;
  find: (request: FindRequest) => void;
  /** Hide the bar and end the search, clearing its highlights. */
  close: () => void;
  onOpened: (callback: (event: FindBarOpened) => void) => () => void;
  onResult: (callback: (result: FindResult) => void) => () => void;
}

/** The app's current look, to hand the bar when opening it. */
export function themeSnapshot(root: HTMLElement = document.documentElement): FindBarTheme {
  return {
    dark: root.classList.contains("dark"),
    colorScheme: root.getAttribute("data-color-scheme"),
  };
}

/** Make the bar page look like the app that opened it. */
export function applyTheme(
  theme: FindBarTheme,
  root: HTMLElement = document.documentElement,
): void {
  root.classList.toggle("dark", theme.dark);
  if (theme.colorScheme) root.setAttribute("data-color-scheme", theme.colorScheme);
  else root.removeAttribute("data-color-scheme");
}
