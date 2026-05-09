import {
  createContext,
  useContext,
  useEffect,
  useRef,
  type ReactNode,
} from "react";
import { EventBus, initEventBus, type EventMap } from "./events";

const EventBusContext = createContext<EventBus | null>(null);

export function EventBusProvider({
  devMode,
  children,
}: {
  devMode?: boolean;
  children: ReactNode;
}) {
  const bus = initEventBus(devMode);
  return (
    <EventBusContext.Provider value={bus}>{children}</EventBusContext.Provider>
  );
}

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
  const handlerRef = useRef(handler);
  handlerRef.current = handler;
  useEffect(() => {
    return bus.on(event, ((payload: EventMap[K]) =>
      handlerRef.current(payload)) as (payload: EventMap[K]) => void);
  }, [bus, event]);
}
