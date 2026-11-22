import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";
import { DesktopTerminalSocket, desktopTerminalSocket } from "../desktop-terminal-socket";

type Ev = { id: string; type: "open" | "message" | "error" | "close"; data?: string; code?: number; reason?: string; wasClean?: boolean };

function fakeBridge(openResult: { success: boolean; id?: string; error?: string } = { success: true, id: "t1" }) {
  let listener: ((e: Ev) => void) | null = null;
  const bridge = {
    open: vi.fn(async (_q: string) => openResult),
    send: vi.fn(),
    close: vi.fn(async () => ({ success: true })),
    onEvent: vi.fn((cb: (e: Ev) => void) => {
      listener = cb;
      return () => {
        listener = null;
      };
    }),
  };
  return { bridge, emit: (e: Ev) => listener?.(e), subscribed: () => listener !== null };
}

const flush = () => new Promise((r) => setTimeout(r, 0));

describe("DesktopTerminalSocket", () => {
  it("behaves like the WebSocket Terminal drives: open, messages, send, close", async () => {
    const { bridge, emit, subscribed } = fakeBridge();
    const ws = new DesktopTerminalSocket(bridge, "workingDir=%2Ftmp");
    const seen: string[] = [];
    ws.onopen = () => seen.push("open");
    ws.onmessage = (e) => seen.push(`msg:${e.data}`);
    ws.onclose = (e) => seen.push(`close:${e.code}:${e.reason}`);
    await flush();
    expect(bridge.open).toHaveBeenCalledWith("workingDir=%2Ftmp");
    expect(ws.readyState).toBe(WebSocket.CONNECTING);

    ws.send("too early");
    expect(bridge.send).not.toHaveBeenCalled();

    emit({ id: "other", type: "open" });
    expect(ws.readyState).toBe(WebSocket.CONNECTING);
    emit({ id: "t1", type: "open" });
    expect(ws.readyState).toBe(WebSocket.OPEN);
    emit({ id: "t1", type: "message", data: '{"type":"init"}' });
    ws.send('{"type":"input","data":"ls"}');
    expect(bridge.send).toHaveBeenCalledWith("t1", '{"type":"input","data":"ls"}');

    ws.close(1000);
    expect(bridge.close).toHaveBeenCalledWith("t1", 1000, undefined);
    emit({ id: "t1", type: "close", code: 1000, reason: "", wasClean: true });
    expect(ws.readyState).toBe(WebSocket.CLOSED);
    expect(subscribed()).toBe(false);
    expect(seen).toEqual(["open", 'msg:{"type":"init"}', "close:1000:"]);
  });

  it("reports a relay that could not open as an error then a 1006 close", async () => {
    const { bridge } = fakeBridge({ success: false, error: "not signed in" });
    const ws = new DesktopTerminalSocket(bridge, "");
    const seen: string[] = [];
    ws.onerror = () => seen.push("error");
    ws.onclose = (e) => seen.push(`close:${e.code}:${e.reason}`);
    await flush();
    expect(seen).toEqual(["error", "close:1006:not signed in"]);
    expect(ws.readyState).toBe(WebSocket.CLOSED);
  });

  it("replays the events that arrived before the relay's open() reply", async () => {
    const { bridge, emit } = fakeBridge();
    let reply!: (r: { success: boolean; id?: string }) => void;
    bridge.open.mockImplementation(() => new Promise((r) => (reply = r)));
    const ws = new DesktopTerminalSocket(bridge, "");
    const seen: string[] = [];
    ws.onopen = () => seen.push("open");
    ws.onmessage = (e) => seen.push(`msg:${e.data}`);
    ws.onclose = (e) => seen.push(`close:${e.code}`);
    // Main opened the upstream socket and relayed its first frames before
    // the invoke reply reached the renderer.
    emit({ id: "t1", type: "open" });
    emit({ id: "other", type: "message", data: "not ours" });
    emit({ id: "t1", type: "message", data: '{"type":"init"}' });
    expect(seen).toEqual([]);
    reply({ success: true, id: "t1" });
    await flush();
    expect(ws.readyState).toBe(WebSocket.OPEN);
    expect(seen).toEqual(["open", 'msg:{"type":"init"}']);
    emit({ id: "t1", type: "message", data: "later" });
    expect(seen).toEqual(["open", 'msg:{"type":"init"}', "msg:later"]);
  });

  it("an early close is replayed too, and releases the subscription", async () => {
    const { bridge, emit, subscribed } = fakeBridge();
    let reply!: (r: { success: boolean; id?: string }) => void;
    bridge.open.mockImplementation(() => new Promise((r) => (reply = r)));
    const ws = new DesktopTerminalSocket(bridge, "");
    const seen: string[] = [];
    ws.onclose = (e) => seen.push(`close:${e.code}:${e.reason}`);
    emit({ id: "t1", type: "open" });
    emit({ id: "t1", type: "close", code: 1011, reason: "upstream gone", wasClean: false });
    reply({ success: true, id: "t1" });
    await flush();
    expect(seen).toEqual(["close:1011:upstream gone"]);
    expect(ws.readyState).toBe(WebSocket.CLOSED);
    expect(subscribed()).toBe(false);
    expect(bridge.close).not.toHaveBeenCalled();
  });

  it("a close before the relay answered closes it once it does", async () => {
    const { bridge } = fakeBridge();
    const ws = new DesktopTerminalSocket(bridge, "");
    ws.close(4000, "unmounted");
    await flush();
    expect(bridge.close).toHaveBeenCalledWith("t1", 4000, "unmounted");
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
    expect(bridge.open).toHaveBeenCalledWith("workingDir=%2Fx");
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
