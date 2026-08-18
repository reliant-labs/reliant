/**
 * Forge Event Bus — typed pub/sub for imperative cross-cutting actions.
 *
 * Extend by merging into the EventMap interface:
 * ```typescript
 * declare module "@/lib/events" {
 *   interface EventMap {
 *     "editor:focusNode": { nodeId: string };
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
  // Toast
  "toast:show": ToastEvent;
  "toast:dismiss": { id?: string };

  // Navigation
  "navigate": { path: string };

  // Auth lifecycle
  "auth:expired": undefined;
  "auth:login": undefined;
  "auth:logout": undefined;

  // Network events
  "network:error": { status: number; message: string };
  "network:unauthorized": undefined;

  // App lifecycle
  "app:ready": undefined;

  // --- Reliant-specific events ---

  // Streaming events
  "stream:started": { chatId: string; thread?: string };
  "stream:completed": { chatId: string; thread?: string };

  // Workspace events
  "worktree:updated": { worktreeId?: string };
  "project:updated": { projectId?: string };

  // Process events
  "process:updated": { processId: string; status: string };

  // Refetch events (replaces refetchStore pub/sub)
  "refetch:worktreeChanges": { entityId?: string };
  "refetch:workflowExecutions": { entityId?: string };
  "refetch:configHealth": { entityId?: string };
  "refetch:planTasks": { entityId?: string };
  "refetch:fileTree": { entityId?: string };

  // The agent drained these mailbox rows into the transcript. Published from
  // the chat-update stream in the same synchronous task that commits the
  // resulting messages, so the pending-queue strip lets go of them in the
  // same React commit the transcript entries appear in.
  "agentMailbox:drained": { chatId: string; thread: string; messageIds: string[] };

  // Daemon
  "daemon:heartbeat": undefined;

  // GitHub credential sync lifecycle
  "github-credential:syncing": undefined;
  "github-credential:succeeded": { trigger: string; attempt: number };
  "github-credential:failed": { trigger: string; attempts: number; error: string };

  // API key lifecycle
  "api-key:saved": { provider: string };
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

// Singleton
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
      "EventBus not initialized — call initEventBus() in Providers first",
    );
  }
  return _bus;
}