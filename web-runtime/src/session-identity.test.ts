// The contract of the automatic user identity: ask /auth/session with the
// cookie, set the subject (and only the subject), re-ask only when the answer
// can have changed, and leave the user alone on any answer that says nothing.
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { isAuthRoute, watchSessionIdentity } from "./session-identity.js";
import {
  disableTelemetry,
  resetTrackState,
  setTelemetrySink,
  type TelemetrySink,
} from "./track.js";

const SESSION_URL = "http://api.test/auth/session";

function sink() {
  return {
    track: vi.fn<TelemetrySink["track"]>(),
    setUser: vi.fn<TelemetrySink["setUser"]>(),
    disable: vi.fn<TelemetrySink["disable"]>(),
  };
}

type Listener = () => void;
const docListeners = new Map<string, Listener[]>();
const doc = { visibilityState: "visible" as DocumentVisibilityState };

function stubDom() {
  docListeners.clear();
  doc.visibilityState = "visible";
  vi.stubGlobal("window", {});
  vi.stubGlobal("document", {
    get visibilityState() {
      return doc.visibilityState;
    },
    addEventListener: (type: string, fn: Listener) =>
      docListeners.set(type, [...(docListeners.get(type) ?? []), fn]),
    removeEventListener: (type: string, fn: Listener) =>
      docListeners.set(
        type,
        (docListeners.get(type) ?? []).filter((l) => l !== fn),
      ),
  });
}

function becomeVisible() {
  doc.visibilityState = "visible";
  for (const fn of docListeners.get("visibilitychange") ?? []) fn();
}

// fetch answering with each body in turn (the last one repeats).
function answer(...bodies: Array<{ status?: number; body?: unknown } | Error>) {
  let i = 0;
  const fetchMock = vi.fn(async () => {
    const next = bodies[Math.min(i++, bodies.length - 1)];
    if (next instanceof Error) {
      throw next;
    }
    return new Response(JSON.stringify(next?.body ?? {}), {
      status: next?.status ?? 200,
      headers: { "Content-Type": "application/json" },
    });
  });
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

beforeEach(() => {
  resetTrackState();
  stubDom();
});

afterEach(() => {
  resetTrackState();
  vi.unstubAllGlobals();
});

describe("watchSessionIdentity", () => {
  it("asks once with the cookie and sets only the subject", async () => {
    const s = sink();
    setTelemetrySink(s);
    const fetchMock = answer({
      body: {
        authenticated: true,
        subject: "2834719",
        email: "jane@example.com",
        name: "Jane",
      },
    });
    await watchSessionIdentity({ sessionUrl: SESSION_URL }).settled();

    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(fetchMock).toHaveBeenCalledWith(SESSION_URL, {
      credentials: "include",
      headers: { Accept: "application/json" },
    });
    expect(s.setUser.mock.calls).toEqual([["2834719"]]);
  });

  it("never sets a subject that looks like an email", async () => {
    const s = sink();
    setTelemetrySink(s);
    answer({ body: { authenticated: true, subject: "jane@example.com" } });
    await watchSessionIdentity({ sessionUrl: SESSION_URL }).settled();
    expect(s.setUser).not.toHaveBeenCalled();
  });

  it("changes nothing on any non-answer or revoke before it has set a user", async () => {
    for (const reply of [
      { status: 404, body: { error: "not found" } },
      { status: 401, body: { authenticated: true, subject: "x" } },
      { status: 403, body: {} },
      new TypeError("Failed to fetch"),
      { body: { user: "someone" } },
      { body: { authenticated: true } },
      { body: "nope" },
    ]) {
      const s = sink();
      setTelemetrySink(s);
      answer(reply);
      const w = watchSessionIdentity({ sessionUrl: SESSION_URL });
      await w.settled();
      w.stop();
      expect(s.setUser, JSON.stringify(reply)).not.toHaveBeenCalled();
    }
  });

  it("clears a user it set once on a revoke or a non-answer, and sets one named later", async () => {
    for (const reply of [
      { body: { authenticated: false } },
      { status: 401, body: { error: "unauthenticated" } },
      { status: 403, body: {} },
      { status: 404, body: { error: "not found" } },
      { status: 500, body: {} },
      new TypeError("Failed to fetch"),
      { body: { user: "someone" } },
      { body: { authenticated: true } },
      { body: "nope" },
    ]) {
      const s = sink();
      setTelemetrySink(s);
      answer({ body: { authenticated: true, subject: "u1" } }, reply);
      const w = watchSessionIdentity({ sessionUrl: SESSION_URL });
      await w.settled();
      w.refresh(); // the revoke or non-answer: clears
      await w.settled();
      w.refresh(); // the same again: nothing more to clear
      await w.settled();
      expect(s.setUser.mock.calls, JSON.stringify(reply)).toEqual([
        ["u1"],
        [""],
      ]);
      w.stop();
    }

    const s = sink();
    setTelemetrySink(s);
    answer(
      { body: { authenticated: true, subject: "u1" } },
      { status: 401, body: {} },
      { body: { authenticated: true, subject: "u2" } },
    );
    const w = watchSessionIdentity({ sessionUrl: SESSION_URL });
    await w.settled();
    w.refresh();
    await w.settled();
    w.refresh();
    await w.settled();
    expect(s.setUser.mock.calls).toEqual([["u1"], [""], ["u2"]]);
  });

  it("writes nothing for a visitor who never signed in", async () => {
    const s = sink();
    setTelemetrySink(s);
    answer({ body: { authenticated: false } });
    await watchSessionIdentity({ sessionUrl: SESSION_URL }).settled();
    expect(s.setUser).not.toHaveBeenCalled();
  });

  it("re-asks on moving into or out of /auth/*, never on other navigations", async () => {
    const s = sink();
    setTelemetrySink(s);
    const fetchMock = answer(
      { body: { authenticated: false } },
      { body: { authenticated: true, subject: "u1" } },
      { body: { authenticated: false } },
    );
    const w = watchSessionIdentity({ sessionUrl: SESSION_URL });
    await w.settled();

    w.onRoute("/auth/sign-in", null); // the install report: no re-ask
    w.onRoute("/home", "/auth/sign-in"); // signed in
    await w.settled();
    w.onRoute("/orders", "/home");
    w.onRoute("/orders/:id", "/orders");
    await w.settled();
    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(s.setUser.mock.calls).toEqual([["u1"]]);

    w.onRoute("/auth/sign-in", "/orders/:id"); // signed out
    await w.settled();
    expect(fetchMock).toHaveBeenCalledTimes(3);
    expect(s.setUser.mock.calls).toEqual([["u1"], [""]]);
  });

  it("re-asks when the tab becomes visible, and writes an unchanged user once", async () => {
    const s = sink();
    setTelemetrySink(s);
    const fetchMock = answer({ body: { authenticated: true, subject: "u1" } });
    const w = watchSessionIdentity({ sessionUrl: SESSION_URL });
    await w.settled();
    doc.visibilityState = "hidden";
    for (const fn of docListeners.get("visibilitychange") ?? []) fn();
    becomeVisible();
    await w.settled();

    expect(fetchMock).toHaveBeenCalledTimes(2);
    expect(s.setUser.mock.calls).toEqual([["u1"]]);
  });

  it("keeps only the newest answer when two race", async () => {
    const s = sink();
    setTelemetrySink(s);
    const resolvers: Array<(r: Response) => void> = [];
    vi.stubGlobal(
      "fetch",
      vi.fn(
        () => new Promise<Response>((resolve) => void resolvers.push(resolve)),
      ),
    );
    const w = watchSessionIdentity({ sessionUrl: SESSION_URL });
    w.refresh();
    const body = (subject: string) =>
      new Response(JSON.stringify({ authenticated: true, subject }));
    // The newer request answers first, then the older one, stale.
    resolvers[1]?.(body("new"));
    resolvers[0]?.(body("stale"));
    await w.settled();

    expect(s.setUser.mock.calls).toEqual([["new"]]);
  });

  it("stops asking on stop() and after disableTelemetry()", async () => {
    const s = sink();
    setTelemetrySink(s);
    const fetchMock = answer({ body: { authenticated: true, subject: "u1" } });
    const w = watchSessionIdentity({ sessionUrl: SESSION_URL });
    w.stop();
    await w.settled();
    becomeVisible();
    w.refresh();
    expect(fetchMock).toHaveBeenCalledTimes(1);
    expect(s.setUser).not.toHaveBeenCalled();
    expect(docListeners.get("visibilitychange")).toEqual([]);

    disableTelemetry();
    // A disabled page asks nothing: refresh() returns before fetch().
    watchSessionIdentity({ sessionUrl: SESSION_URL });
    expect(fetchMock).toHaveBeenCalledTimes(1);
  });
});

describe("isAuthRoute", () => {
  it("is /auth and everything under it", () => {
    expect(isAuthRoute("/auth")).toBe(true);
    expect(isAuthRoute("/auth/sign-in")).toBe(true);
    expect(isAuthRoute("/authors")).toBe(false);
    expect(isAuthRoute("/")).toBe(false);
    expect(isAuthRoute(null)).toBe(false);
  });
});
