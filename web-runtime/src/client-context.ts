// What a page reports about the client it runs in: its client hints, its
// viewport and screen, and, inside a desktop app, the app's versions.
//
// ── What the page reports, and what it does not ──────────────────────────
//
// The structured device facts a console filters on — browser name and
// version, OS name and version, device type — are NOT reported here. The
// ingest side derives them from the request's own User-Agent and the
// browser-set `Sec-CH-UA*` headers (which page script cannot set), and
// deletes any `user_agent.*`, `os.*` or `reliant.device.type` a page sent
// before stamping its own. A page can claim anything; the request's headers
// are the browser's.
//
// What only the page can know is reported here, sanitized, under the
// conventions that say it is page-reported:
//
//   browser.brands, browser.platform, browser.mobile   (resource)
//       navigator.userAgentData, the low-entropy client hints. Brands are
//       "Name Version" strings with GREASE brands ("Not A;Brand") dropped.
//       Absent outside Chromium.
//   app.desktop.version, app.desktop.electron.version,
//   app.desktop.chromium.version                       (resource)
//       inside a desktop app, from the preload bridge (below).
//   app.viewport.width/height, app.screen.width/height (span attributes)
//       CSS pixels, refreshed (debounced) when the window resizes.
//
// browser.language is not repeated: the SDK already stamps it.
//
// Nothing here is high-entropy: no getHighEntropyValues() call (it prompts
// no one but is a fingerprinting surface), no model, no full version list.
//
// ── The desktop bridge ───────────────────────────────────────────────────
//
// A desktop app's renderer is a page like any other; the app's own version is
// known only to its main process. The preload exposes it with
//
//   contextBridge.exposeInMainWorld("forgeDesktop", {
//     appVersion,                         // app.getVersion(), from main
//     electronVersion: process.versions.electron,
//     chromiumVersion: process.versions.chrome,
//   });
//
// and the runtime reads `window.forgeDesktop` once at startup. A sandboxed
// preload cannot require this package, so the contract is the global, not a
// helper. Each value must look like a version (VERSION_RE) or it is dropped.

/** What a desktop preload exposes as `window.forgeDesktop`. */
export interface ForgeDesktopInfo {
  appVersion?: string;
  electronVersion?: string;
  chromiumVersion?: string;
}

/** A version string: `1.8.16`, `1.8.16-dev+7d787d80`, `33.2.1`. */
const VERSION_RE = /^[0-9A-Za-z][0-9A-Za-z.+_-]{0,63}$/;

/** A client-hint brand or platform: printable, short, no separators that would confuse a join. */
const HINT_RE = /^[\x20-\x7e]{1,48}$/;

/** At most this many brands are kept. Browsers send three. */
const MAX_BRANDS = 5;

/** Larger than any real screen in CSS pixels; anything beyond is a lie or a bug. */
const MAX_PIXELS = 32768;

/** How long the window must stop resizing before the new size is stamped. */
export const VIEWPORT_DEBOUNCE_MS = 1000;

interface UADataBrand {
  brand?: unknown;
  version?: unknown;
}

interface UAData {
  brands?: unknown;
  mobile?: unknown;
  platform?: unknown;
}

function version(value: unknown): string | undefined {
  return typeof value === "string" && VERSION_RE.test(value)
    ? value
    : undefined;
}

function hint(value: unknown): string | undefined {
  if (typeof value !== "string") {
    return undefined;
  }
  const v = value.trim();
  return HINT_RE.test(v) ? v : undefined;
}

/**
 * A GREASE brand: Chromium's deliberately bogus entry ("Not A;Brand",
 * "Not)A;Brand", "Not_A Brand") that exists to break naive parsers.
 */
function isGrease(brand: string): boolean {
  return /^not[^a-z]*a[^a-z]*brand$/i.test(brand.replace(/\s+/g, " ").trim());
}

/**
 * The resource attributes a page reports about its client: client hints
 * and, inside a desktop app, its versions. Pure given its inputs; every value
 * is validated and anything malformed is dropped, never truncated into
 * something it was not.
 */
export function clientResourceAttributes(source: {
  userAgentData?: unknown;
  desktop?: unknown;
}): Record<string, string> {
  const out: Record<string, string> = {};
  const ua = source.userAgentData as UAData | undefined;
  if (ua && typeof ua === "object") {
    if (Array.isArray(ua.brands)) {
      const brands: string[] = [];
      for (const b of ua.brands.slice(0, MAX_BRANDS * 2) as UADataBrand[]) {
        const name = hint(b?.brand);
        const v = version(b?.version);
        if (name && v && !isGrease(name) && !name.includes(",")) {
          brands.push(`${name} ${v}`);
        }
      }
      if (brands.length > 0) {
        // Resource attributes are strings on this SDK; a comma-joined list
        // is the encoding (brand names never contain one; see above).
        out["browser.brands"] = brands.slice(0, MAX_BRANDS).join(", ");
      }
    }
    const platform = hint(ua.platform);
    if (platform) {
      out["browser.platform"] = platform;
    }
    if (typeof ua.mobile === "boolean") {
      out["browser.mobile"] = String(ua.mobile);
    }
  }
  const desktop = source.desktop as ForgeDesktopInfo | undefined;
  if (desktop && typeof desktop === "object") {
    const app = version(desktop.appVersion);
    const electron = version(desktop.electronVersion);
    const chromium = version(desktop.chromiumVersion);
    if (app) out["app.desktop.version"] = app;
    if (electron) out["app.desktop.electron.version"] = electron;
    if (chromium) out["app.desktop.chromium.version"] = chromium;
  }
  return out;
}

function pixels(value: unknown): string | undefined {
  if (typeof value !== "number" || !Number.isFinite(value)) {
    return undefined;
  }
  const n = Math.round(value);
  return n > 0 && n <= MAX_PIXELS ? String(n) : undefined;
}

/** The span attributes naming this page's viewport and screen, in CSS pixels. */
export function viewportAttributes(win: {
  innerWidth?: number;
  innerHeight?: number;
  screen?: { width?: number; height?: number };
}): Record<string, string> {
  const out: Record<string, string> = {};
  const vw = pixels(win.innerWidth);
  const vh = pixels(win.innerHeight);
  const sw = pixels(win.screen?.width);
  const sh = pixels(win.screen?.height);
  // A dimension is kept only with its pair: "1440 x ?" is not a size.
  if (vw && vh) {
    out["app.viewport.width"] = vw;
    out["app.viewport.height"] = vh;
  }
  if (sw && sh) {
    out["app.screen.width"] = sw;
    out["app.screen.height"] = sh;
  }
  return out;
}

/** The client attributes of the running page: never throws, empty without a DOM. */
export function pageClientResourceAttributes(): Record<string, string> {
  if (typeof window === "undefined") {
    return {};
  }
  try {
    return clientResourceAttributes({
      userAgentData:
        typeof navigator !== "undefined"
          ? (navigator as unknown as { userAgentData?: unknown }).userAgentData
          : undefined,
      desktop: (window as unknown as { forgeDesktop?: unknown }).forgeDesktop,
    });
  } catch {
    return {};
  }
}

/**
 * Calls `stamp` with the page's viewport attributes now and again, debounced,
 * whenever the window is resized to a different size. Returns the function
 * that stops watching. Never throws.
 */
export function watchViewport(
  stamp: (attributes: Record<string, string>) => void,
  win: Window | undefined = typeof window !== "undefined" ? window : undefined,
): () => void {
  if (!win) {
    return () => {};
  }
  let last = "";
  const emit = () => {
    try {
      const attrs = viewportAttributes(win);
      const key = JSON.stringify(attrs);
      if (key !== last && Object.keys(attrs).length > 0) {
        last = key;
        stamp(attrs);
      }
    } catch {
      // Telemetry must never break the app.
    }
  };
  emit();
  if (typeof win.addEventListener !== "function") {
    return () => {};
  }
  let timer: ReturnType<typeof setTimeout> | undefined;
  const onResize = () => {
    if (timer !== undefined) clearTimeout(timer);
    timer = setTimeout(emit, VIEWPORT_DEBOUNCE_MS);
  };
  win.addEventListener("resize", onResize, { passive: true });
  return () => {
    if (timer !== undefined) clearTimeout(timer);
    win.removeEventListener("resize", onResize);
  };
}
