import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { DesktopTerminalSocket, desktopTerminalSocket } from "../desktop-terminal-socket";

type Ev = { id: string; type: "open" | "message" | "error" | "close"; data?: string; code?: number; reason?: string; wasClean?: boolean };
type OpenResult = { success: boolean; id?: string; error?: string };

/**
 * A preload bridge. By default main answers with the id the socket sent
 * (today's main process); `reply` replaces that answer. With `hold`, the
 * answer waits until release() — events sent meanwhile are the race the
 * socket must survive.
 */
function fakeBridge(opts: { reply?: (requested?: string) => OpenResult; hold?: boolean } = {}) {
  let listener: ((e: Ev) => void) | null = null;
  let release: () => void = () => {};
  const held = opts.hold ? new Promise<void>((r) => (release = r)) : Promise.resolve();
  const bridge = {
    open: vi.fn(async (_q: string, id?: string): Promise<OpenResult> => {
      await held;
      return opts.reply ? opts.reply(id) : { success: true, id };
    }),
    send: vi.fn(),
    close: vi.fn(async () => ({ success: true })),
    onEvent: vi.fn((cb: (e: Ev) => void) => {
      listener = cb;
      return () => {
        listener = null;
      };
    }),
  };
  return {
    bridge,
    emit: (e: Ev) => listener?.(e),
    subscribed: () => listener !== null,
    release: () => release(),
    /** The id the socket sent with open(). */
    sentId: () => bridge.open.mock.calls[0]?.[1] as string,
  };
}

const flush = () => new Promise((r) => setTimeout(r, 0));

/** Another terminal's traffic: renderer-named and main-named ids alike. */
function emitForeign(emit: (e: Ev) => void, n: number) {
  for (let i = 0; i < n; i++) {
    const id = i % 2 ? `c${String(i).padStart(32, "0")}` : `t${i + 100}`;
    emit({ id, type: i % 7 === 0 ? "open" : "message", data: i % 5 === 0 ? '{"type":"init"}' : `output ${i}` });
  }
}

describe("DesktopTerminalSocket", () => {
  it("behaves like the WebSocket Terminal drives: open, messages, send, close", async () => {
    const { bridge, emit, subscribed, sentId } = fakeBridge();
    const ws = new DesktopTerminalSocket(bridge, "workingDir=%2Ftmp");
    const seen: string[] = [];
    ws.onopen = () => seen.push("open");
    ws.onmessage = (e) => seen.push(`msg:${e.data}`);
    ws.onclose = (e) => seen.push(`close:${e.code}:${e.reason}`);
    await flush();
    const id = sentId();
    expect(id).toMatch(/^c[0-9a-f-]{32,36}$/i);
    expect(bridge.open).toHaveBeenCalledWith("workingDir=%2Ftmp", id);
    expect(ws.readyState).toBe(WebSocket.CONNECTING);

    ws.send("too early");
    expect(bridge.send).not.toHaveBeenCalled();

    emit({ id: "other", type: "open" });
    expect(ws.readyState).toBe(WebSocket.CONNECTING);
    emit({ id, type: "open" });
    expect(ws.readyState).toBe(WebSocket.OPEN);
    emit({ id, type: "message", data: '{"type":"init"}' });
    ws.send('{"type":"input","data":"ls"}');
    expect(bridge.send).toHaveBeenCalledWith(id, '{"type":"input","data":"ls"}');

    ws.close(1000);
    expect(bridge.close).toHaveBeenCalledWith(id, 1000, undefined);
    emit({ id, type: "close", code: 1000, reason: "", wasClean: true });
    expect(ws.readyState).toBe(WebSocket.CLOSED);
    expect(subscribed()).toBe(false);
    expect(seen).toEqual(["open", 'msg:{"type":"init"}', "close:1000:"]);
  });

  it("each socket sends an id of its own", async () => {
    const a = fakeBridge();
    const b = fakeBridge();
    new DesktopTerminalSocket(a.bridge, "");
    new DesktopTerminalSocket(b.bridge, "");
    await flush();
    expect(a.sentId()).not.toEqual(b.sentId());
  });

  it("reports a relay that could not open as an error then a 1006 close", async () => {
    const { bridge } = fakeBridge({ reply: () => ({ success: false, error: "not signed in" }) });
    const ws = new DesktopTerminalSocket(bridge, "");
    const seen: string[] = [];
    ws.onerror = () => seen.push("error");
    ws.onclose = (e) => seen.push(`close:${e.code}:${e.reason}`);
    await flush();
    expect(seen).toEqual(["error", "close:1006:not signed in"]);
    expect(ws.readyState).toBe(WebSocket.CLOSED);
  });

  it("takes its open and init before the open() reply, however much foreign traffic came first", async () => {
    const { bridge, emit, release, sentId } = fakeBridge({ hold: true });
    const ws = new DesktopTerminalSocket(bridge, "");
    const seen: string[] = [];
    ws.onopen = () => seen.push("open");
    ws.onmessage = (e) => seen.push(`msg:${e.data}`);
    await flush();
    const id = sentId();
    // Main opened the upstream socket and relayed frames while other
    // terminals of this window were busy, all before the invoke reply.
    emitForeign(emit, 5000);
    emit({ id, type: "open" });
    emitForeign(emit, 5000);
    emit({ id, type: "message", data: '{"type":"init"}' });
    expect(ws.readyState).toBe(WebSocket.OPEN);
    expect(seen).toEqual(["open", 'msg:{"type":"init"}']);
    release();
    await flush();
    emit({ id, type: "message", data: "later" });
    expect(seen).toEqual(["open", 'msg:{"type":"init"}', "msg:later"]);
  });

  it("an upstream close before the open() reply ends it and releases the subscription", async () => {
    const { bridge, emit, subscribed, release, sentId } = fakeBridge({ hold: true });
    const ws = new DesktopTerminalSocket(bridge, "");
    const seen: string[] = [];
    ws.onclose = (e) => seen.push(`close:${e.code}:${e.reason}`);
    await flush();
    const id = sentId();
    emitForeign(emit, 5000);
    emit({ id, type: "open" });
    emit({ id, type: "close", code: 1011, reason: "upstream gone", wasClean: false });
    emitForeign(emit, 5000);
    expect(seen).toEqual(["close:1011:upstream gone"]);
    expect(ws.readyState).toBe(WebSocket.CLOSED);
    expect(subscribed()).toBe(false);
    release();
    await flush();
    expect(bridge.close).not.toHaveBeenCalled();
  });

  it("a close() before the open() reply closes it once main answers", async () => {
    const { bridge, emit, release, sentId } = fakeBridge({ hold: true });
    const ws = new DesktopTerminalSocket(bridge, "");
    await flush();
    ws.close(4000, "unmounted");
    expect(ws.readyState).toBe(WebSocket.CLOSING);
    emit({ id: sentId(), type: "open" });
    expect(ws.readyState, "an open after close() does not reopen it").toBe(WebSocket.CLOSING);
    expect(bridge.close).not.toHaveBeenCalled();
    release();
    await flush();
    expect(bridge.close).toHaveBeenCalledWith(sentId(), 4000, "unmounted");
  });

  describe("a main process that names the socket itself", () => {
    const named = { reply: () => ({ success: true, id: "t9" }), hold: true };

    it("replays its open and init after the reply, past any foreign traffic, and drops output", async () => {
      const { bridge, emit, release } = fakeBridge(named);
      const ws = new DesktopTerminalSocket(bridge, "");
      const seen: string[] = [];
      ws.onopen = () => seen.push("open");
      ws.onmessage = (e) => seen.push(`msg:${e.data}`);
      await flush();
      emitForeign(emit, 5000);
      emit({ id: "t9", type: "open" });
      emitForeign(emit, 5000);
      emit({ id: "t9", type: "message", data: '{"type":"init"}' });
      emit({ id: "t9", type: "message", data: "early output" });
      expect(seen).toEqual([]);
      release();
      await flush();
      expect(ws.readyState).toBe(WebSocket.OPEN);
      expect(seen).toEqual(["open", 'msg:{"type":"init"}']);
      ws.send("x");
      expect(bridge.send).toHaveBeenCalledWith("t9", "x");
    });

    it("replays a close that came before the reply", async () => {
      const { bridge, emit, subscribed, release } = fakeBridge(named);
      const ws = new DesktopTerminalSocket(bridge, "");
      const seen: string[] = [];
      ws.onclose = (e) => seen.push(`close:${e.code}`);
      await flush();
      emit({ id: "t9", type: "open" });
      emitForeign(emit, 5000);
      emit({ id: "t9", type: "close", code: 1011, reason: "", wasClean: false });
      release();
      await flush();
      expect(seen).toEqual(["close:1011"]);
      expect(subscribed()).toBe(false);
      expect(bridge.close).not.toHaveBeenCalled();
    });
  });
});

describe("desktopTerminalSocket", () => {
  const PACKAGED = { protocol: "app:" };
  beforeEach(() => {
    vi.stubGlobal("__FORGE_CONFIG__", undefined);
  });
  afterEach(() => {
    vi.unstubAllGlobals();
    // @ts-expect-error test cleanup
    delete window.electronAPI;
  });

  function packagedForge(withBridge: boolean) {
    (window as unknown as { __FORGE_CONFIG__: object }).__FORGE_CONFIG__ = { AUTH_PROVIDER: "forge" };
    const { bridge } = fakeBridge();
    (window as unknown as { electronAPI: object }).electronAPI = {
      forgeAuth: { signIn: vi.fn(), signOut: vi.fn(), sessionChanged: vi.fn(), ...(withBridge ? { terminal: bridge } : {}) },
    };
    return bridge;
  }

  it("relays in the packaged forge desktop app, and never passes a token to the main process", async () => {
    const bridge = packagedForge(true);
    const ws = desktopTerminalSocket(new URLSearchParams({ workingDir: "/x", token: "jwt" }), PACKAGED);
    expect(ws).not.toBeNull();
    await flush();
    expect(bridge.open).toHaveBeenCalledWith("workingDir=%2Fx", expect.stringMatching(/^c/));
  });

  it("is null on the web, under Supabase, and in a desktop build without the bridge", () => {
    packagedForge(true);
    expect(desktopTerminalSocket(new URLSearchParams(), { protocol: "https:" }), "the web / dev server").toBeNull();
    packagedForge(false);
    expect(desktopTerminalSocket(new URLSearchParams(), PACKAGED), "no bridge").toBeNull();
    packagedForge(true);
    (window as unknown as { __FORGE_CONFIG__: object }).__FORGE_CONFIG__ = { AUTH_PROVIDER: "supabase" };
    expect(desktopTerminalSocket(new URLSearchParams(), PACKAGED), "Supabase").toBeNull();
  });
});
