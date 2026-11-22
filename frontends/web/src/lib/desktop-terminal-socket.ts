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
  open: (query: string, id?: string) => Promise<{ success: boolean; id?: string; error?: string }>;
  send: (id: string, data: string) => void;
  close: (id: string, code?: number, reason?: string) => Promise<{ success: boolean }>;
  onEvent: (callback: (event: TerminalEvent) => void) => () => void;
};

const CONNECTING = 0;
const OPEN = 1;
const CLOSING = 2;
const CLOSED = 3;

/**
 * This socket's own id, sent with open(): the main process uses it as the id
 * of every event of the socket (forge-auth.js TerminalRelays), so the socket
 * knows its events from the first one, before the open() reply. Same shape
 * as main's TERMINAL_CLIENT_ID; never one of main's own ids (t1, t2, …).
 */
function newClientId(): string {
  const c = globalThis.crypto;
  if (typeof c?.randomUUID === "function") return `c${c.randomUUID()}`;
  const bytes = c.getRandomValues(new Uint8Array(16));
  return `c${Array.from(bytes, (b) => b.toString(16).padStart(2, "0")).join("")}`;
}

/** Another renderer socket's id: never this socket's, whatever main answers. */
const isClientId = (id: string) => id.startsWith("c");

/**
 * Whether an event decides the socket's state: open, init (the terminal's
 * first frame), error and close. Output is not.
 */
function isLifecycle(event: TerminalEvent): boolean {
  if (event.type !== "message") return true;
  try {
    return (JSON.parse(event.data ?? "") as { type?: unknown })?.type === "init";
  } catch {
    return false;
  }
}

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

  // This socket's id: chosen here and sent with open(), so the events the
  // main process sends before its open() reply (it opens the upstream socket
  // first, and events travel on another IPC channel) are recognised as they
  // arrive, and every other terminal's are ignored at once.
  private id: string;
  private unsubscribe: (() => void) | null = null;
  private closeRequested: { code?: number; reason?: string } | null = null;
  // Whether the open() reply has arrived.
  private answered = false;
  // Only for a main process that ignores the id sent with open() and names
  // the socket in its reply (fallback): until then, the lifecycle events of
  // the sockets main named itself are held, unbounded but few (four per
  // socket at most), and those of the reply's id replayed. Output is never
  // held, and events of another renderer socket (a client id) are dropped.
  private early: TerminalEvent[] = [];

  private readonly bridge: TerminalBridge;

  constructor(bridge: TerminalBridge, query: string) {
    this.bridge = bridge;
    this.id = newClientId();
    // Subscribe before opening, so no event of this socket can be missed.
    this.unsubscribe = bridge.onEvent((event) => this.receive(event));
    bridge.open(query, this.id).then(
      (result) => {
        const early = this.early;
        this.early = [];
        this.answered = true;
        if (!result.success || !result.id) {
          this.finish(1006, result.error || "the desktop app could not open the terminal", true);
          return;
        }
        if (result.id !== this.id) {
          // A main process that named the socket itself.
          this.id = result.id;
          for (const event of early) this.handle(event);
        }
        if (this.closeRequested && this.readyState !== CLOSED) {
          void bridge.close(this.id, this.closeRequested.code, this.closeRequested.reason);
        }
      },
      () => {
        this.early = [];
        this.answered = true;
        this.finish(1006, "the desktop app could not open the terminal", true);
      },
    );
  }

  private receive(event: TerminalEvent) {
    if (event.id === this.id) {
      this.handle(event);
      return;
    }
    if (!this.answered && !isClientId(event.id) && isLifecycle(event)) this.early.push(event);
  }

  private handle(event: TerminalEvent) {
    if (event.id !== this.id || this.readyState === CLOSED) return;
    switch (event.type) {
      case "open":
        // A close() asked for before the open() reply keeps it CLOSING.
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
    if (this.readyState !== OPEN) return;
    this.bridge.send(this.id, data);
  }

  close(code?: number, reason?: string) {
    if (this.readyState === CLOSING || this.readyState === CLOSED) return;
    this.readyState = CLOSING;
    if (!this.answered) {
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
