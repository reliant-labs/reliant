/**
 * Forge Event Bus — typed pub/sub for imperative cross-cutting actions.
 *
 * Default events are provided. Extend by merging into the EventMap interface:
 * Packs (e.g. auth) may add their own events here during scaffold.
 *
 * ```typescript
 * // In your code:
 * declare module "@/lib/events" {
 *   interface EventMap {
 *     "editor:focusNode": { nodeId: string };
 *     "workflow:runRequested": { workflowId: string };
 *   }
 * }
 * ```
 */

export interface ToastEvent {
  message: string;
  variant?: "success" | "error" | "warning" | "info";
  duration?: number;
}

// Default event map — users extend this via TS declaration merging
export interface EventMap {
  "toast:show": ToastEvent;
  "toast:dismiss": { id?: string };
  navigate: { path: string };
  "auth:expired": undefined;
  "auth:login": undefined;
  "auth:logout": undefined;

  // Network events
  "network:error": { status: number; message: string };
  "network:unauthorized": undefined;

  // App lifecycle (primarily used by mobile, but available everywhere)
  "app:ready": undefined;
}

type EventHandler<T> = (payload: T) => void;

export class EventBus {
  private listeners = new Map<string, Set<EventHandler<unknown>>>();
  private devMode: boolean;

  constructor(devMode = false) {
    this.devMode = devMode;
  }

  emit<K extends keyof EventMap>(
    event: K,
    ...args: EventMap[K] extends undefined ? [] : [EventMap[K]]
  ): void {
    if (this.devMode) {
      console.debug(`[event] ${String(event)}`, args[0] ?? "");
    }
    const handlers = this.listeners.get(event as string);
    if (handlers) {
      for (const handler of handlers) {
        handler(args[0]);
      }
    }
  }

  on<K extends keyof EventMap>(
    event: K,
    handler: EventHandler<EventMap[K]>,
  ): () => void {
    const key = event as string;
    if (!this.listeners.has(key)) {
      this.listeners.set(key, new Set());
    }
    this.listeners.get(key)!.add(handler as EventHandler<unknown>);

    // Return unsubscribe function
    return () => {
      this.listeners.get(key)?.delete(handler as EventHandler<unknown>);
    };
  }

  off<K extends keyof EventMap>(
    event: K,
    handler: EventHandler<EventMap[K]>,
  ): void {
    this.listeners
      .get(event as string)
      ?.delete(handler as EventHandler<unknown>);
  }
}

// Singleton — initialized in EventBusProvider (src/lib/event-context.tsx)
let _bus: EventBus | null = null;

export function initEventBus(devMode = false): EventBus {
  if (!_bus) {
    _bus = new EventBus(devMode);
  }
  return _bus;
}

export function getEventBus(): EventBus {
  if (!_bus) {
    throw new Error(
      "EventBus not initialized — call initEventBus() in EventBusProvider first",
    );
  }
  return _bus;
}

/**
 * emitToast — fire-and-forget toast emission for infrastructure code
 * (e.g. the MutationCache error chokepoint in query-client.ts) that may
 * run before Providers initialized the bus (unit tests, SSR prerender).
 * A typed no-op in that window beats the old `try { ... } catch {}`
 * pattern, which also swallowed real listener errors.
 */
export function emitToast(toast: ToastEvent): void {
  if (!_bus) return;
  _bus.emit("toast:show", toast);
}
