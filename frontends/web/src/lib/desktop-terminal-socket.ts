/**
 * The terminal WebSocket in the packaged desktop app with forge sign-in.
 *
 * There the page is `app://bundle` and the session is a cookie the MAIN
 * process holds (electron/src/forge-auth.js): the renderer can neither open
 * `wss://<api>/api/v2/terminal/ws` with it nor go through the app:// handler,
 * which cannot upgrade a connection. So the main process opens the socket and
 * relays its frames over IPC (forge-auth.js TerminalRelays, for why a relay
 * rather than a proxied upgrade). This module is the renderer half: an object
 * shaped like the WebSocket Terminal.tsx drives (onopen, onmessage, onerror,
 * onclose, send, close, readyState), over the preload's
 * `window.electronAPI.forgeAuth.terminal` bridge.
 *
 * Everywhere else — the web, the dev server, Supabase sign-in, a desktop
 * build without the bridge — `desktopTerminalSocket` answers null and the
 * caller opens a real WebSocket, as before.
 */
import { isDesktopForgeSignIn } from "./desktop-forge-sign-in";

type TerminalEvent = {
  id: string;
  type: "open" | "message" | "error" | "close";
  data?: string;
  code?: number;
  reason?: string;
  wasClean?: boolean;
};

type TerminalBridge = {
  open: (query: string) => Promise<{ success: boolean; id?: string; error?: string }>;
  send: (id: string, data: string) => void;
  close: (id: string, code?: number, reason?: string) => Promise<{ success: boolean }>;
  onEvent: (callback: (event: TerminalEvent) => void) => () => void;
};

const CONNECTING = 0;
const OPEN = 1;
const CLOSING = 2;
const CLOSED = 3;

// The most events held while the relay's open() reply is pending. A real
// socket sends a handful (open, init) before that reply lands; the cap only
// bounds what other sockets' events could cost in the meantime.
const PENDING_EVENTS_MAX = 1024;

function terminalBridge(): TerminalBridge | undefined {
  if (typeof window === "undefined") return undefined;
  return window.electronAPI?.forgeAuth?.terminal as TerminalBridge | undefined;
}

/**
 * A WebSocket-shaped terminal connection relayed by the desktop main process.
 * Exported for tests; callers use desktopTerminalSocket.
 */
export class DesktopTerminalSocket {
  readonly CONNECTING = CONNECTING;
  readonly OPEN = OPEN;
  readonly CLOSING = CLOSING;
  readonly CLOSED = CLOSED;

  readyState: number = CONNECTING;
  onopen: ((event: Event) => void) | null = null;
  onmessage: ((event: MessageEvent) => void) | null = null;
  onerror: ((event: Event) => void) | null = null;
  onclose: ((event: CloseEvent) => void) | null = null;

  private id: string | null = null;
  private unsubscribe: (() => void) | null = null;
  private closeRequested: { code?: number; reason?: string } | null = null;
  // Events that arrived before open() told this socket its id. The main
  // process opens the upstream socket BEFORE it replies, and sends events on
  // another IPC channel, so open/init/close can beat the reply; they are kept
  // here and replayed, in order, once the id is known.
  private pending: TerminalEvent[] | null = [];

  private readonly bridge: TerminalBridge;

  constructor(bridge: TerminalBridge, query: string) {
    this.bridge = bridge;
    // Subscribe before opening, so an event that arrives with (or before)
    // the answer cannot be missed; events for other sockets are ignored by id.
    this.unsubscribe = bridge.onEvent((event) => this.receive(event));
    bridge.open(query).then(
      (result) => {
        if (!result.success || !result.id) {
          this.pending = null;
          this.finish(1006, result.error || "the desktop app could not open the terminal", true);
          return;
        }
        this.id = result.id;
        const early = this.pending ?? [];
        this.pending = null;
        for (const event of early) this.handle(event);
        if (this.closeRequested && this.readyState !== CLOSED) {
          void bridge.close(result.id, this.closeRequested.code, this.closeRequested.reason);
        }
      },
      () => {
        this.pending = null;
        this.finish(1006, "the desktop app could not open the terminal", true);
      },
    );
  }

  private receive(event: TerminalEvent) {
    if (this.pending) {
      // Not yet known whose it is: hold it (oldest dropped past the cap).
      if (this.pending.length >= PENDING_EVENTS_MAX) this.pending.shift();
      this.pending.push(event);
      return;
    }
    this.handle(event);
  }

  private handle(event: TerminalEvent) {
    if (this.id === null || event.id !== this.id || this.readyState === CLOSED) return;
    switch (event.type) {
      case "open":
        // A close() asked for before the id was known keeps it CLOSING.
        if (this.readyState !== CONNECTING) break;
        this.readyState = OPEN;
        this.onopen?.(new Event("open"));
        break;
      case "message":
        this.onmessage?.(new MessageEvent("message", { data: event.data ?? "" }));
        break;
      case "error":
        this.onerror?.(new Event("error"));
        break;
      case "close":
        this.finish(event.code ?? 1005, event.reason ?? "", false, event.wasClean ?? false);
        break;
    }
  }

  private finish(code: number, reason: string, error: boolean, wasClean = false) {
    if (this.readyState === CLOSED) return;
    this.readyState = CLOSED;
    this.unsubscribe?.();
    this.unsubscribe = null;
    if (error) this.onerror?.(new Event("error"));
    this.onclose?.(new CloseEvent("close", { code, reason, wasClean }));
  }

  /** Text frames only (the terminal protocol is JSON); dropped unless open, as Terminal checks first. */
  send(data: string) {
    if (this.readyState !== OPEN || this.id === null) return;
    this.bridge.send(this.id, data);
  }

  close(code?: number, reason?: string) {
    if (this.readyState === CLOSING || this.readyState === CLOSED) return;
    this.readyState = CLOSING;
    if (this.id === null) {
      this.closeRequested = { code, reason };
      return;
    }
    void this.bridge.close(this.id, code, reason);
  }
}

/**
 * The relayed terminal socket when this is the packaged desktop app on forge
 * sign-in, else null (open a real WebSocket). `params` is the terminal's query;
 * the main process keeps only the terminal's own parameters, never a token.
 */
export function desktopTerminalSocket(
  params: URLSearchParams,
  page: { protocol: string } = typeof window === "undefined" ? { protocol: "" } : window.location,
): WebSocket | null {
  // Only the packaged app (app://bundle): the dev server's Vite proxy
  // carries the session cookie on a real same-origin upgrade.
  if (page.protocol !== "app:") return null;
  if (!isDesktopForgeSignIn()) return null;
  const bridge = terminalBridge();
  if (!bridge) return null;
  params.delete("token");
  return new DesktopTerminalSocket(bridge, params.toString()) as unknown as WebSocket;
}
