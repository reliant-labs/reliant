// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// The session-cookie fallback, proven against the SDK's REAL session module
// (@hyperdx/otel-web dist/cjs/session.js, the code bundled into
// @hyperdx/browser) on a document whose cookie jar drops every write, the way
// an Electron `app://` renderer does. Without the fallback that module mints a
// new session id on every periodic check; with it, one id holds until the
// SDK's own 15-minute inactivity or 4-hour limit ends it.
import { createRequire } from "node:module";
import { dirname, join } from "node:path";

import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import {
  cookiesPersist,
  installSessionCookieFallback,
  RUM_SESSION_COOKIE,
  RUM_SESSION_STORAGE_KEY,
  type SessionStore,
} from "./session-cookie-fallback.js";

interface SdkSession {
  initSessionTracking(
    instanceId: string,
    eventTarget?: unknown,
    domain?: string,
  ): { deinit(): void };
  updateSessionStatus(): void;
  getRumSessionId(): string | undefined;
}

// The package's `exports` names only its index; the session module is reached
// by file path, next to the index the resolver returns.
const require = createRequire(import.meta.url);
const sdkSessionPath = join(
  dirname(require.resolve("@hyperdx/otel-web")),
  "session.js",
);

const MINUTE = 60_000;
const T0 = Date.UTC(2026, 10, 1, 12, 0, 0);

type Listener = () => void;

/** A document whose cookie jar keeps nothing (a non-cookieable scheme). */
class DroppingDocument {
  readonly listeners = new Map<string, Listener[]>();
  readonly written: string[] = [];
  get cookie(): string {
    return "";
  }
  set cookie(value: string) {
    this.written.push(value);
  }
  addEventListener(type: string, fn: Listener) {
    this.listeners.set(type, [...(this.listeners.get(type) ?? []), fn]);
  }
  dispatch(type: string) {
    for (const fn of this.listeners.get(type) ?? []) fn();
  }
}

/** A document whose cookie jar works (max-age ignored: the tests never age it). */
class KeepingDocument {
  private jar = new Map<string, string>();
  get cookie(): string {
    return [...this.jar].map(([k, v]) => `${k}=${v}`).join("; ");
  }
  set cookie(value: string) {
    const [pair = "", ...attrs] = value.split(";");
    const eq = pair.indexOf("=");
    const name = pair.slice(0, eq).trim();
    if (attrs.some((a) => a.trim().toLowerCase() === "max-age=0")) {
      this.jar.delete(name);
    } else {
      this.jar.set(name, pair.slice(eq + 1));
    }
  }
}

function memoryStore(): SessionStore & { map: Map<string, string> } {
  const map = new Map<string, string>();
  return {
    map,
    getItem: (k) => map.get(k) ?? null,
    setItem: (k, v) => void map.set(k, v),
    removeItem: (k) => void map.delete(k),
  };
}

/** A fresh copy of the SDK session module, bound to `doc` as its document. */
function loadSdkSession(doc: object): SdkSession {
  vi.stubGlobal("document", doc);
  vi.stubGlobal("window", { self: 1, top: 1 });
  delete require.cache[sdkSessionPath];
  return require(sdkSessionPath) as SdkSession;
}

let deinit: (() => void) | undefined;

beforeEach(() => {
  vi.useFakeTimers();
  vi.setSystemTime(T0);
});

afterEach(() => {
  deinit?.();
  deinit = undefined;
  vi.useRealTimers();
  vi.unstubAllGlobals();
});

describe("cookiesPersist", () => {
  it("is false where writes are dropped and true where they are kept", () => {
    expect(cookiesPersist(new DroppingDocument())).toBe(false);
    const keeping = new KeepingDocument();
    expect(cookiesPersist(keeping)).toBe(true);
    // The probe cleans up after itself.
    expect(keeping.cookie).toBe("");
  });

  it("is false where cookie access throws (a sandboxed document)", () => {
    const sandboxed = {
      get cookie(): string {
        throw new Error("SecurityError");
      },
      set cookie(_v: string) {
        throw new Error("SecurityError");
      },
    };
    expect(cookiesPersist(sandboxed)).toBe(false);
  });
});

describe("the SDK session on a document that drops cookies", () => {
  it("without the fallback, gets a new id on every periodic check", () => {
    const doc = new DroppingDocument();
    const sdk = loadSdkSession(doc);
    deinit = sdk.initSessionTracking("instance").deinit;
    const first = sdk.getRumSessionId();
    vi.advanceTimersByTime(MINUTE);
    const second = sdk.getRumSessionId();
    vi.advanceTimersByTime(MINUTE);
    expect(new Set([first, second, sdk.getRumSessionId()]).size).toBe(3);
  });

  it("with the fallback, keeps one id across checks while the user is active", () => {
    const doc = new DroppingDocument();
    const store = memoryStore();
    expect(
      installSessionCookieFallback(doc as unknown as Document, store),
    ).toBe(true);
    const sdk = loadSdkSession(doc);
    deinit = sdk.initSessionTracking("instance").deinit;
    const id = sdk.getRumSessionId();
    expect(id).toMatch(/^[0-9a-f]{32}$/);
    for (let minute = 1; minute <= 60; minute++) {
      if (minute % 10 === 0) doc.dispatch("click");
      vi.advanceTimersByTime(MINUTE);
      expect(sdk.getRumSessionId()).toBe(id);
    }
    // The session lives in storage; the browser's jar never saw it.
    expect(store.map.get(RUM_SESSION_STORAGE_KEY)).toContain(id);
    expect(
      doc.written.some((c) => c.startsWith(`${RUM_SESSION_COOKIE}=`)),
    ).toBe(false);
  });

  it("with the fallback, keeps one id across checks with no activity inside 15 minutes", () => {
    const doc = new DroppingDocument();
    installSessionCookieFallback(doc as unknown as Document, memoryStore());
    const sdk = loadSdkSession(doc);
    deinit = sdk.initSessionTracking("instance").deinit;
    const id = sdk.getRumSessionId();
    vi.advanceTimersByTime(14 * MINUTE);
    expect(sdk.getRumSessionId()).toBe(id);
  });

  it("ends the session after 15 minutes without activity", () => {
    const doc = new DroppingDocument();
    installSessionCookieFallback(doc as unknown as Document, memoryStore());
    const sdk = loadSdkSession(doc);
    deinit = sdk.initSessionTracking("instance").deinit;
    const id = sdk.getRumSessionId();
    vi.advanceTimersByTime(16 * MINUTE);
    const next = sdk.getRumSessionId();
    expect(next).not.toBe(id);
    // And the new session is as stable as the first.
    vi.advanceTimersByTime(5 * MINUTE);
    expect(sdk.getRumSessionId()).toBe(next);
  });

  it("ends the session after 4 hours however active the user is", () => {
    const doc = new DroppingDocument();
    installSessionCookieFallback(doc as unknown as Document, memoryStore());
    const sdk = loadSdkSession(doc);
    deinit = sdk.initSessionTracking("instance").deinit;
    const id = sdk.getRumSessionId();
    for (let minute = 1; minute < 240; minute++) {
      doc.dispatch("click");
      vi.advanceTimersByTime(MINUTE);
    }
    expect(sdk.getRumSessionId()).toBe(id);
    doc.dispatch("click");
    vi.advanceTimersByTime(2 * MINUTE);
    expect(sdk.getRumSessionId()).not.toBe(id);
  });

  it("shares one session between windows of the origin, as a cookie jar does", () => {
    const store = memoryStore();
    const a = new DroppingDocument();
    installSessionCookieFallback(a as unknown as Document, store);
    const sdkA = loadSdkSession(a);
    const stopA = sdkA.initSessionTracking("a").deinit;
    const id = sdkA.getRumSessionId();
    stopA();

    const b = new DroppingDocument();
    installSessionCookieFallback(b as unknown as Document, store);
    const sdkB = loadSdkSession(b);
    deinit = sdkB.initSessionTracking("b").deinit;
    expect(sdkB.getRumSessionId()).toBe(id);
  });
});

describe("installSessionCookieFallback", () => {
  it("leaves a document whose cookies work untouched", () => {
    const doc = new KeepingDocument();
    expect(
      installSessionCookieFallback(doc as unknown as Document, memoryStore()),
    ).toBe(false);
    expect(Object.getOwnPropertyDescriptor(doc, "cookie")).toBeUndefined();
  });

  it("does nothing without working storage", () => {
    const broken: SessionStore = {
      getItem: () => null,
      setItem: () => {
        throw new Error("QuotaExceededError");
      },
      removeItem: () => {},
    };
    const doc = new DroppingDocument();
    expect(
      installSessionCookieFallback(doc as unknown as Document, broken),
    ).toBe(false);
    expect(installSessionCookieFallback(doc as unknown as Document, null)).toBe(
      false,
    );
    expect(Object.getOwnPropertyDescriptor(doc, "cookie")).toBeUndefined();
  });

  it("passes every other cookie to the browser", () => {
    const doc = new DroppingDocument();
    installSessionCookieFallback(doc as unknown as Document, memoryStore());
    doc.written.length = 0;
    (doc as { cookie: string }).cookie = "theme=dark; path=/";
    expect(doc.written).toEqual(["theme=dark; path=/"]);
  });

  it("honours max-age, expires and deletion on the session cookie", () => {
    const doc = new DroppingDocument() as unknown as { cookie: string };
    const store = memoryStore();
    installSessionCookieFallback(doc as unknown as Document, store);
    doc.cookie = `${RUM_SESSION_COOKIE}=abc; path=/;max-age=60;SameSite=Strict`;
    expect(doc.cookie).toBe(`${RUM_SESSION_COOKIE}=abc`);
    vi.advanceTimersByTime(61_000);
    expect(doc.cookie).toBe("");
    expect(store.map.has(RUM_SESSION_STORAGE_KEY)).toBe(false);

    doc.cookie = `${RUM_SESSION_COOKIE}=def; expires=${new Date(Date.now() + 5_000).toUTCString()}`;
    expect(doc.cookie).toBe(`${RUM_SESSION_COOKIE}=def`);
    doc.cookie = `${RUM_SESSION_COOKIE}=def; max-age=0`;
    expect(doc.cookie).toBe("");
  });

  it("is idempotent", () => {
    const doc = new DroppingDocument();
    const store = memoryStore();
    expect(
      installSessionCookieFallback(doc as unknown as Document, store),
    ).toBe(true);
    expect(
      installSessionCookieFallback(doc as unknown as Document, store),
    ).toBe(true);
    (doc as unknown as { cookie: string }).cookie =
      `${RUM_SESSION_COOKIE}=x; max-age=60`;
    expect((doc as unknown as { cookie: string }).cookie).toBe(
      `${RUM_SESSION_COOKIE}=x`,
    );
  });
});
