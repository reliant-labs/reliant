/**
 * Compose mode: the on-screen keyboard is up on the mobile surface.
 *
 * The visible viewport is at its smallest while the user writes the next turn
 * (~400px on a phone), so chrome that is worth its space at rest — the pinned
 * prompt, ambient status lines — steps aside. See
 * `dev-docs/design/mobile-chat-redesign.md` §10.
 *
 * `MobileLayout` is the one place that measures the visual viewport and
 * provides this; everything below reads it instead of subscribing to
 * `visualViewport` again, so every band agrees on when compose mode starts.
 * Outside a provider (the desktop shell) it is always false.
 */

import { createContext, useContext, type ReactNode } from "react";

const ComposeModeContext = createContext(false);

export function ComposeModeProvider({
  keyboardOpen,
  children,
}: {
  keyboardOpen: boolean;
  children: ReactNode;
}) {
  return (
    <ComposeModeContext.Provider value={keyboardOpen}>
      {children}
    </ComposeModeContext.Provider>
  );
}

/** True while the on-screen keyboard is open on the mobile surface. */
export function useComposeMode(): boolean {
  return useContext(ComposeModeContext);
}
