import { createContext, useContext, useEffect, useRef } from "react";

import type { EventBus, EventMap } from "./events";

// The event bus context and the hooks that read it. <EventBusProvider>
// (./event-context.tsx) supplies the bus; these live apart from it so that
// file exports only a component — React Fast Refresh can hot-swap a module
// only when every export is one (eslint-plugin-react-refresh's
// only-export-components).

export const EventBusContext = createContext<EventBus | null>(null);

export function useEventBus(): EventBus {
  const bus = useContext(EventBusContext);
  if (!bus) {
    throw new Error("useEventBus must be used within <EventBusProvider>");
  }
  return bus;
}

/**
 * Subscribe to an event with automatic cleanup on unmount.
 */
export function useEvent<K extends keyof EventMap>(
  event: K,
  handler: (payload: EventMap[K]) => void,
): void {
  const bus = useEventBus();

  // The ref holds the LATEST handler so the subscription below does not have
  // to depend on it. A caller almost always passes an inline arrow, which is
  // a new function identity every render; depending on it directly would
  // unsubscribe and resubscribe the bus on every single render.
  //
  // The assignment lives in an effect rather than in the render body because
  // writing a ref during render is a side effect in the render phase — React
  // is entitled to render without committing (it does, when a render is
  // thrown away or replayed), which would publish a handler that never went
  // live. This effect has no dependency array on purpose: it runs after every
  // committed render, which is exactly when "latest" changes.
  const handlerRef = useRef(handler);
  useEffect(() => {
    handlerRef.current = handler;
  });

  useEffect(() => {
    return bus.on(event, ((payload: EventMap[K]) =>
      handlerRef.current(payload)) as (payload: EventMap[K]) => void);
  }, [bus, event]);
}
