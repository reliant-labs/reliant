"use client";

// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// The toast host: the queue and its lifecycle, with NO opinion about how a
// toast looks or where the events come from. The app supplies both —
// `subscribe` (its event bus) and `render` (its own toast component from the
// forkable component library). That is what keeps this forge-owned file
// independent of files the app is free to restyle, rename, or delete.
import {
  useCallback,
  useEffect,
  useMemo,
  useState,
  type ReactElement,
  type ReactNode,
} from "react";

export type RuntimeToastVariant = "success" | "error" | "warning" | "info";

/** A toast as emitted — no identity yet; the host assigns one. */
export interface RuntimeToastPayload {
  message: string;
  variant?: RuntimeToastVariant;
  duration?: number;
}

/** A toast the host is currently holding. */
export interface RuntimeToast extends RuntimeToastPayload {
  id: string;
}

/** What the host hands the app so the app can push events into it. */
export interface RuntimeToastSink {
  /** Enqueue a toast. */
  onShow: (toast: RuntimeToastPayload) => void;
  /** Dismiss one toast by id, or all of them when id is omitted. */
  onDismiss: (id?: string) => void;
}

export interface RuntimeToastHostProps {
  /**
   * Wire the app's event source to the host: called with the sink, returns an
   * unsubscribe function. Memoize it (useCallback) — the host re-subscribes
   * whenever its identity changes.
   */
  subscribe: (sink: RuntimeToastSink) => () => void;
  /**
   * Render the app's own toast presentation. The runtime owns the queue; the
   * component library owns the markup.
   */
  render: (props: {
    toasts: RuntimeToast[];
    onDismiss: (id: string) => void;
  }) => ReactNode;
}

let seq = 0;
function nextId(): string {
  seq += 1;
  return `t${seq}-${Date.now()}`;
}

export function RuntimeToastHost({
  subscribe,
  render,
}: RuntimeToastHostProps): ReactElement {
  const [toasts, setToasts] = useState<RuntimeToast[]>([]);

  const dismiss = useCallback((id: string) => {
    setToasts((prev) => prev.filter((t) => t.id !== id));
  }, []);

  // Stable across renders: every update is functional, so the sink never
  // needs to close over the current toast list.
  const sink = useMemo<RuntimeToastSink>(
    () => ({
      onShow: (toast) => {
        setToasts((prev) => [...prev, { ...toast, id: nextId() }]);
      },
      onDismiss: (id) => {
        setToasts((prev) => (id ? prev.filter((t) => t.id !== id) : []));
      },
    }),
    [],
  );

  useEffect(() => subscribe(sink), [subscribe, sink]);

  return <>{render({ toasts, onDismiss: dismiss })}</>;
}
