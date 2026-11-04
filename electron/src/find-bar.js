// The desktop app's find-in-page bar, one per window.
//
// A browser tab has its own find bar; the desktop app has no browser chrome to
// provide one, so off a conversation Cmd+F (and Edit › Find) opens this. The
// renderer decides WHEN — the shortcut `findInPage` in config/shortcuts.yaml,
// which loses Cmd+F to the conversation search and the editor's own find where
// those apply — and asks main to open it on the "find-bar:open" channel.
//
// The bar is a WebContentsView of its own laid over the window, loading the
// web bundle's find-bar.html (frontends/web/src/findBar), not a component in
// the page it searches. Measured against Electron 41: webContents.findInPage
// matches the text of the page's own inputs, so an in-page search box counts,
// and lands on, its own query; and every search moves the page's focus to the
// active match, out of the box being typed in. Putting the focus back makes
// the next search start from the box, so "next" never advances. Out of the
// page, the bar keeps its focus and caret, the count is the page's, and next
// and previous walk the matches — which is why Chrome's find bar is browser UI.
//
// Kept free of Electron imports so it is unit-testable: main.js supplies the
// window, a view factory and the bar's URL.

// Channel names. Both preloads repeat them (a sandboxed preload can require
// nothing but electron); test/find-bar.test.js holds them equal.
const FIND_BAR_CHANNELS = Object.freeze({
  // The app's renderer → main: show the bar over this window. (invoke)
  open: "find-bar:open",
  // The bar page → main.
  ready: "find-bar:ready",
  find: "find-bar:find",
  close: "find-bar:close",
  // Main → the bar page.
  opened: "find-bar:opened",
  result: "find-bar:result",
});

// The view: the bar, plus a transparent margin its shadow falls into.
const FIND_BAR_SIZE = Object.freeze({ width: 400, height: 56 });
// Top-right of the window, just under the app's 48px header bar.
const FIND_BAR_OFFSET = Object.freeze({ top: 44, right: 12 });

/** Where the bar's view goes in a window whose content area is `contentBounds`. */
function findBarBounds(contentBounds) {
  const width = Math.min(FIND_BAR_SIZE.width, Math.max(0, contentBounds.width));
  return {
    x: Math.max(0, contentBounds.width - width - FIND_BAR_OFFSET.right),
    y: FIND_BAR_OFFSET.top,
    width,
    height: FIND_BAR_SIZE.height,
  };
}

/** A search request from the bar page, or null for anything malformed. */
function sanitizeFindRequest(raw) {
  if (!raw || typeof raw !== "object" || typeof raw.text !== "string") return null;
  return {
    text: raw.text,
    forward: raw.forward !== false,
    findNext: raw.findNext === true,
    matchCase: raw.matchCase === true,
  };
}

/** The renderer's theme snapshot (src/findBar/protocol.ts), made safe to forward. */
function sanitizeTheme(raw) {
  const theme = raw && typeof raw === "object" ? raw : {};
  return {
    dark: theme.dark === true,
    colorScheme:
      typeof theme.colorScheme === "string" && /^[a-z0-9-]{1,64}$/.test(theme.colorScheme)
        ? theme.colorScheme
        : null,
  };
}

/**
 * One window's find bar.
 *
 *   window      the BrowserWindow: webContents (the page searched),
 *               contentView, getContentBounds, on/removeListener, isDestroyed.
 *   createView  () => a WebContentsView for the bar, its preload set.
 *   url         the bar page (find-bar.html on the app's origin).
 *   log         electron-log or console.
 */
function createFindBarController({ window, createView, url, log = console }) {
  const page = window.webContents;
  let view = null;
  let isOpen = false;
  // Whether the bar page has said it is listening (FIND_BAR_CHANNELS.ready).
  let barReady = false;
  let theme = sanitizeTheme(null);
  // Only the latest search's results reach the bar: typing fast leaves older
  // searches still reporting.
  let latestRequestId = 0;
  let destroyed = false;

  const barContents = () =>
    view && !view.webContents.isDestroyed() ? view.webContents : null;

  const sendToBar = (channel, payload) => {
    const contents = barContents();
    if (contents && barReady) contents.send(channel, payload);
  };

  const placeView = () => {
    if (view) view.setBounds(findBarBounds(window.getContentBounds()));
  };

  const ensureView = () => {
    if (view) return view;
    view = createView();
    barReady = false;
    view.setBackgroundColor("#00000000");
    view.setVisible(false);
    // The bar page never navigates and never opens windows.
    view.webContents.on("will-navigate", (event) => event.preventDefault());
    view.webContents.setWindowOpenHandler(() => ({ action: "deny" }));
    // A crashed or reloaded bar page must say it is ready again before main
    // talks to it.
    view.webContents.on("did-start-loading", () => {
      barReady = false;
    });
    view.webContents.loadURL(url).catch((error) => {
      log.error("[FindBar] Failed to load the find bar:", error);
    });
    return view;
  };

  const onFoundInPage = (_event, result) => {
    if (!isOpen || !result) return;
    // A search that has since been replaced, or ended.
    if (!latestRequestId || result.requestId !== latestRequestId) return;
    sendToBar(FIND_BAR_CHANNELS.result, {
      matches: result.matches | 0,
      activeMatchOrdinal: result.activeMatchOrdinal | 0,
      finalUpdate: result.finalUpdate === true,
    });
  };

  const stopFinding = () => {
    latestRequestId = 0;
    if (!page.isDestroyed()) page.stopFindInPage("clearSelection");
  };

  const onResize = () => placeView();

  // A new document is not the one the bar was searching.
  const onNavigate = () => close();

  page.on("found-in-page", onFoundInPage);
  page.on("did-navigate", onNavigate);
  window.on("resize", onResize);

  /** Show the bar over the window and give it the keyboard. */
  function open(rawTheme) {
    if (destroyed || window.isDestroyed()) return false;
    theme = sanitizeTheme(rawTheme);
    const bar = ensureView();
    // Re-adding a child view raises it above every other one (the embedded
    // browser's tabs included).
    window.contentView.addChildView(bar);
    placeView();
    bar.setVisible(true);
    bar.webContents.focus();
    isOpen = true;
    sendToBar(FIND_BAR_CHANNELS.opened, { theme });
    return true;
  }

  /** The bar page is listening; tell it now if it was opened before it was. */
  function ready() {
    barReady = true;
    if (isOpen) sendToBar(FIND_BAR_CHANNELS.opened, { theme });
  }

  function find(rawRequest) {
    const request = sanitizeFindRequest(rawRequest);
    if (!isOpen || !request || page.isDestroyed()) return;
    if (request.text === "") {
      stopFinding();
      sendToBar(FIND_BAR_CHANNELS.result, { matches: 0, activeMatchOrdinal: 0, finalUpdate: true });
      return;
    }
    const { text, ...options } = request;
    latestRequestId = page.findInPage(text, options);
  }

  /** Hide the bar, clear the highlights, and hand the keyboard back to the page. */
  function close() {
    if (!isOpen) return;
    isOpen = false;
    stopFinding();
    if (view) view.setVisible(false);
    if (!page.isDestroyed()) page.focus();
  }

  function destroy() {
    if (destroyed) return;
    destroyed = true;
    isOpen = false;
    if (!page.isDestroyed()) {
      page.removeListener("found-in-page", onFoundInPage);
      page.removeListener("did-navigate", onNavigate);
    }
    window.removeListener("resize", onResize);
    // A WebContentsView's page outlives its window unless closed.
    const contents = barContents();
    if (contents) contents.close();
    view = null;
  }

  return {
    open,
    ready,
    find,
    close,
    destroy,
    /** Whether `contents` is this bar's page. */
    owns: (contents) => Boolean(contents) && barContents() === contents,
    isOpen: () => isOpen,
  };
}

module.exports = {
  FIND_BAR_CHANNELS,
  FIND_BAR_SIZE,
  findBarBounds,
  sanitizeFindRequest,
  sanitizeTheme,
  createFindBarController,
};
