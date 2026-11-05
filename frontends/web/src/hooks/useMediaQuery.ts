import { useCallback, useSyncExternalStore } from "react";

/** Live `window.matchMedia(query).matches`; false on the server and where matchMedia is missing. */
export function useMediaQuery(query: string): boolean {
  const subscribe = useCallback(
    (onChange: () => void) => {
      const list = typeof window !== "undefined" ? window.matchMedia?.(query) : undefined;
      if (!list) return () => {};
      list.addEventListener("change", onChange);
      return () => list.removeEventListener("change", onChange);
    },
    [query],
  );
  const getSnapshot = useCallback(
    () => typeof window !== "undefined" && !!window.matchMedia?.(query)?.matches,
    [query],
  );
  return useSyncExternalStore(subscribe, getSnapshot, () => false);
}
