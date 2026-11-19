// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// A stable RUM session on an origin that cannot keep cookies.
//
// ── Why ──────────────────────────────────────────────────────────────────
//
// The HyperDX SDK (its otel-web layer) keeps the session ONLY in a cookie,
// `__rum_sid`: `{ id, startTime }`, written with `max-age=900` and renewed
// while the user is active. Every 60 s it reads the cookie back, and when it
// finds none it mints a new session id. There is no other store and no seam
// to supply one: the session module is bundled inside the HyperDX browser package.
//
// Some origins accept a `document.cookie` write and silently keep nothing.
// The one that matters is an Electron renderer served from a custom scheme
// (`app://bundle/`): Chromium treats a non-http(s) scheme as non-cookieable
// even when it is registered `standard` + `secure`, so the write is dropped
// and `session.cookies.set` refuses with EXCLUDE_NONCOOKIEABLE_SCHEME.
// Verified against Electron 41. On such an origin every open window starts a
// new "session" every minute; each one re-uploads a full replay snapshot and
// none of them is a session anyone can watch.
//
// ── What ─────────────────────────────────────────────────────────────────
//
// When, and only when, a probe cookie does not round-trip, the session cookie
// is kept in localStorage instead. An own `cookie` accessor on `document`
// answers reads and writes of `__rum_sid` from localStorage, honouring the
// max-age the SDK writes, and passes every other cookie to the browser
// untouched. The SDK's own rules then hold unchanged: the session expires 15
// minutes after the last activity (max-age=900, renewed on activity) and never
// outlives 4 hours (the startTime inside the value). localStorage is shared by
// every window of the origin, as a cookie jar is, so the windows of one app
// share one session the way the tabs of one browser do.
//
// An origin whose cookies work is left exactly as it was.
//
// ── In memory, below the `full` analytics tier ───────────────────────────
//
// Below `full` (track.ts, "Analytics tiers") nothing may be stored on the
// device, the session cookie included. installRumSessionStore answers the
// same `document.cookie` reads and writes of `__rum_sid` from a variable
// instead, with the same expiry rules, so the SDK still groups this page's
// spans into one session and nothing reaches the cookie jar or storage. The
// session then lasts as long as the page (or its 15-minute inactivity
// window). When consent raises the tier to `full`, moveToDevice() moves the
// running session to the cookie (or, where cookies are dropped, to
// localStorage as above) without starting a new one; moveToMemory() moves it
// back and erases the device copy.

/** The SDK's session cookie (otel-web COOKIE_NAME). */
export const RUM_SESSION_COOKIE = "__rum_sid";

/** Where the session cookie is kept when cookies do not persist. */
export const RUM_SESSION_STORAGE_KEY = "forge.rum.session";

const PROBE_COOKIE = "forge_cookie_probe";

/** The slice of Storage the fallback needs. */
export interface SessionStore {
  getItem(key: string): string | null;
  setItem(key: string, value: string): void;
  removeItem(key: string): void;
}

interface StoredCookie {
  /** The cookie value exactly as the SDK wrote it (URI-encoded JSON). */
  v: string;
  /** Epoch ms at which the cookie expires. */
  exp: number;
}

/**
 * Whether `document.cookie` keeps what is written to it. Writes a short-lived
 * probe cookie, reads it back and removes it.
 */
export function cookiesPersist(doc: Pick<Document, "cookie">): boolean {
  try {
    const token = Math.random().toString(36).slice(2);
    doc.cookie = `${PROBE_COOKIE}=${token}; path=/; max-age=60; SameSite=Strict`;
    const kept = doc.cookie
      .split(";")
      .some((c) => c.trim() === `${PROBE_COOKIE}=${token}`);
    doc.cookie = `${PROBE_COOKIE}=; path=/; max-age=0; SameSite=Strict`;
    return kept;
  } catch {
    // A sandboxed document throws on cookie access: nothing persists.
    return false;
  }
}

/**
 * Keep the SDK's session cookie in `storage` when `doc` cannot keep cookies.
 * Returns whether the fallback is now in place (it was installed by this call,
 * or already was). Idempotent; never throws.
 *
 * Call it before the SDK initialises: the SDK reads the session at init.
 */
export function installSessionCookieFallback(
  doc: Document,
  storage: SessionStore | null,
  now: () => number = Date.now,
): boolean {
  try {
    if (installed.has(doc)) {
      return true;
    }
    if (!storage || !storageWorks(storage) || cookiesPersist(doc)) {
      return false;
    }
    const native = findCookieDescriptor(doc);
    if (!native?.get || !native.set) {
      return false;
    }
    const nativeGet = native.get;
    const nativeSet = native.set;
    Object.defineProperty(doc, "cookie", {
      configurable: true,
      enumerable: true,
      get(this: Document): string {
        const jar = String(nativeGet.call(this) ?? "");
        const session = readStored(storage, now());
        if (session === null) {
          return jar;
        }
        const entry = `${RUM_SESSION_COOKIE}=${session}`;
        return jar ? `${jar}; ${entry}` : entry;
      },
      set(this: Document, value: string) {
        const text = String(value);
        if (!text.startsWith(`${RUM_SESSION_COOKIE}=`)) {
          nativeSet.call(this, text);
          return;
        }
        writeStored(storage, text, now());
      },
    });
    installed.add(doc);
    return true;
  } catch {
    return false;
  }
}

const installed = new WeakSet<object>();

/** Where installRumSessionStore keeps the session now. */
export type RumSessionBackend = "memory" | "cookie" | "storage";

/** The handle installRumSessionStore returns. */
export interface RumSessionStore {
  /** Where the session is kept now. */
  backend(): RumSessionBackend;
  /**
   * Keep the session on the device: the cookie when cookies persist, else
   * `storage`, else (neither works) memory still. The running session moves
   * with it.
   */
  moveToDevice(storage: SessionStore | null): void;
  /** Keep the session in memory and erase the device copy. */
  moveToMemory(): void;
}

/**
 * Answer the SDK's `__rum_sid` cookie from memory (see "In memory" in the
 * header), switchable to the device and back. Starts in memory, after
 * erasing any `__rum_sid` cookie a previous page load left. Call it before
 * the SDK initialises. Returns null when `doc.cookie` cannot be wrapped (the
 * SDK then keeps its own cookie, as before this existed). Never throws.
 */
export function installRumSessionStore(
  doc: Document,
  now: () => number = Date.now,
  options?: {
    /**
     * Keep a session an earlier page load left on the device (the caller
     * moves the store there at once, in the `full` tier): it is not erased,
     * so a reload continues the session instead of starting a new one.
     */
    keepDeviceSession?: boolean;
  },
): RumSessionStore | null {
  try {
    const native = findCookieDescriptor(doc);
    if (!native?.get || !native.set || installed.has(doc)) {
      return null;
    }
    const nativeGet = native.get;
    const nativeSet = native.set;
    const jar = {
      get cookie(): string {
        return String(nativeGet.call(doc) ?? "");
      },
      set cookie(value: string) {
        nativeSet.call(doc, value);
      },
    };
    const memory = memoryStore();
    let backend: RumSessionBackend = "memory";
    let device: SessionStore | null = null;
    // The store the accessor reads in "memory" and "storage".
    const held = () => (backend === "storage" && device ? device : memory);

    if (
      !options?.keepDeviceSession &&
      nativeSessionValue(jar.cookie) !== null
    ) {
      jar.cookie = `${RUM_SESSION_COOKIE}=; path=/; max-age=0`;
    }
    Object.defineProperty(doc, "cookie", {
      configurable: true,
      enumerable: true,
      get(this: Document): string {
        const text = jar.cookie;
        if (backend === "cookie") {
          return text;
        }
        const session = readStored(held(), now());
        if (session === null) {
          return text;
        }
        const entry = `${RUM_SESSION_COOKIE}=${session}`;
        return text ? `${text}; ${entry}` : entry;
      },
      set(this: Document, value: string) {
        const text = String(value);
        if (
          backend === "cookie" ||
          !text.startsWith(`${RUM_SESSION_COOKIE}=`)
        ) {
          jar.cookie = text;
          return;
        }
        writeStored(held(), text, now());
      },
    });
    installed.add(doc);

    // The running session as a stored cookie, wherever it is kept now.
    const current = (): StoredCookie | null => {
      if (backend === "cookie") {
        const v = nativeSessionValue(jar.cookie);
        return v === null
          ? null
          : { v, exp: now() + SESSION_COOKIE_FALLBACK_MS };
      }
      return readRaw(held(), now());
    };

    return {
      backend: () => backend,
      moveToDevice(storage) {
        try {
          if (backend !== "memory") {
            return;
          }
          const session = current();
          if (cookiesPersist(jar)) {
            backend = "cookie";
            memory.removeItem(RUM_SESSION_STORAGE_KEY);
            if (session) {
              const seconds = Math.max(
                1,
                Math.ceil((session.exp - now()) / 1000),
              );
              jar.cookie = `${RUM_SESSION_COOKIE}=${session.v}; path=/; max-age=${seconds}; SameSite=Strict`;
            }
          } else if (storage && storageWorks(storage)) {
            backend = "storage";
            device = storage;
            memory.removeItem(RUM_SESSION_STORAGE_KEY);
            if (session) {
              storage.setItem(RUM_SESSION_STORAGE_KEY, JSON.stringify(session));
            }
          }
        } catch {
          // The session stays where it was.
        }
      },
      moveToMemory() {
        try {
          if (backend === "memory") {
            return;
          }
          const session = current();
          if (backend === "cookie") {
            jar.cookie = `${RUM_SESSION_COOKIE}=; path=/; max-age=0`;
          } else {
            device?.removeItem(RUM_SESSION_STORAGE_KEY);
          }
          backend = "memory";
          device = null;
          if (session) {
            memory.setItem(RUM_SESSION_STORAGE_KEY, JSON.stringify(session));
          }
        } catch {
          // The session stays where it was.
        }
      },
    };
  } catch {
    return null;
  }
}

function memoryStore(): SessionStore {
  const values = new Map<string, string>();
  return {
    getItem: (key) => values.get(key) ?? null,
    setItem: (key, value) => void values.set(key, value),
    removeItem: (key) => void values.delete(key),
  };
}

// The `__rum_sid` value in a cookie jar string, or null.
function nativeSessionValue(text: string): string | null {
  for (const part of text.split(";")) {
    const entry = part.trim();
    if (entry.startsWith(`${RUM_SESSION_COOKIE}=`)) {
      const value = entry.slice(RUM_SESSION_COOKIE.length + 1);
      return value === "" ? null : value;
    }
  }
  return null;
}

/** The accessor `doc.cookie` resolves to: its own, or its prototype chain's. */
function findCookieDescriptor(doc: object): PropertyDescriptor | undefined {
  for (
    let o: object | null = doc;
    o;
    o = Object.getPrototypeOf(o) as object | null
  ) {
    const d = Object.getOwnPropertyDescriptor(o, "cookie");
    if (d) {
      return d;
    }
  }
  return undefined;
}

function storageWorks(storage: SessionStore): boolean {
  const key = `${RUM_SESSION_STORAGE_KEY}.probe`;
  try {
    storage.setItem(key, "1");
    const ok = storage.getItem(key) === "1";
    storage.removeItem(key);
    return ok;
  } catch {
    return false;
  }
}

function readStored(storage: SessionStore, at: number): string | null {
  return readRaw(storage, at)?.v ?? null;
}

function readRaw(storage: SessionStore, at: number): StoredCookie | null {
  try {
    const raw = storage.getItem(RUM_SESSION_STORAGE_KEY);
    if (!raw) {
      return null;
    }
    const stored = JSON.parse(raw) as Partial<StoredCookie>;
    if (typeof stored.v !== "string" || typeof stored.exp !== "number") {
      storage.removeItem(RUM_SESSION_STORAGE_KEY);
      return null;
    }
    if (stored.exp <= at) {
      // Expired, as the browser would have expired the cookie.
      storage.removeItem(RUM_SESSION_STORAGE_KEY);
      return null;
    }
    return { v: stored.v, exp: stored.exp };
  } catch {
    return null;
  }
}

/**
 * Store a `__rum_sid=<value>; …attributes` write. `max-age` (seconds) or
 * `expires` set the expiry; `max-age=0` or a past expiry deletes. A write with
 * neither is a session cookie, which here lasts the SDK's inactivity window:
 * the SDK always sends max-age, so this is only a defined answer for a write
 * it does not make.
 */
function writeStored(storage: SessionStore, text: string, at: number): void {
  const [pair = "", ...attributes] = text.split(";");
  const value = pair.slice(RUM_SESSION_COOKIE.length + 1).trim();
  let exp: number | null = null;
  for (const attribute of attributes) {
    const eq = attribute.indexOf("=");
    const name = (eq < 0 ? attribute : attribute.slice(0, eq))
      .trim()
      .toLowerCase();
    const raw = eq < 0 ? "" : attribute.slice(eq + 1).trim();
    if (name === "max-age") {
      const seconds = Number(raw);
      if (Number.isFinite(seconds)) {
        // max-age wins over expires, as in a browser.
        exp = at + seconds * 1000;
        break;
      }
    } else if (name === "expires" && exp === null) {
      const when = Date.parse(raw);
      if (!Number.isNaN(when)) {
        exp = when;
      }
    }
  }
  try {
    if (value === "" || (exp !== null && exp <= at)) {
      storage.removeItem(RUM_SESSION_STORAGE_KEY);
      return;
    }
    const stored: StoredCookie = {
      v: value,
      exp: exp ?? at + SESSION_COOKIE_FALLBACK_MS,
    };
    storage.setItem(RUM_SESSION_STORAGE_KEY, JSON.stringify(stored));
  } catch {
    // Storage full or revoked: the SDK sees no cookie and starts a session,
    // as it would have without the fallback.
  }
}

/** The SDK's inactivity window (otel-web InactivityTimeoutSeconds). */
const SESSION_COOKIE_FALLBACK_MS = 15 * 60 * 1000;
