/**
 * React binding for the surface capability map (see `lib/surface.ts`).
 *
 * Components ask "may I render this?" rather than "am I on a phone?", so the
 * mobile and embed scope boundaries stay declarative and greppable.
 *
 * The default is `desktop`, which means the existing app keeps every
 * capability without touching a single call site — only code that opts into a
 * narrower surface by rendering `<SurfaceProvider>` sees anything change.
 */

import { createContext, useContext, useMemo, type ReactNode } from "react";
import {
  capabilitiesFor,
  type Surface,
  type SurfaceCapabilities,
} from "./surface";

interface SurfaceContextValue {
  surface: Surface;
  capabilities: SurfaceCapabilities;
}

// Defaulting to desktop (rather than undefined) means components outside any
// provider — i.e. the entire existing ADE — behave exactly as before.
const SurfaceContext = createContext<SurfaceContextValue>({
  surface: "desktop",
  capabilities: capabilitiesFor("desktop"),
});

export function SurfaceProvider({
  surface,
  children,
}: {
  surface: Surface;
  children: ReactNode;
}) {
  const value = useMemo(
    () => ({ surface, capabilities: capabilitiesFor(surface) }),
    [surface],
  );
  return (
    <SurfaceContext.Provider value={value}>{children}</SurfaceContext.Provider>
  );
}

/** Current surface identifier. Prefer `useCapability` where you can. */
export function useSurface(): Surface {
  return useContext(SurfaceContext).surface;
}

/**
 * Whether a capability is available here.
 *
 * Prefer this over `useSurface() === 'mobile'` — it states the requirement
 * ("this needs attachments") instead of a proxy for it, so when the embed
 * surface arrives the call site already answers correctly.
 */
export function useCapability(name: keyof SurfaceCapabilities): boolean {
  return useContext(SurfaceContext).capabilities[name];
}

/** Full capability set, for components branching on several at once. */
export function useCapabilities(): SurfaceCapabilities {
  return useContext(SurfaceContext).capabilities;
}
