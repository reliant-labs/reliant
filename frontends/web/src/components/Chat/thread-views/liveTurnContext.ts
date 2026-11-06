import { createContext, useContext } from "react";

/**
 * True while the simplified thread shows a live work fold for the turn in
 * progress. That fold's header is then the turn's status line (for example,
 * "Reading ChatInput.tsx"), so the transcript footer's generic thinking line
 * would only repeat it. Provided around the footer by InterleavedTimeline;
 * false everywhere else, so nothing outside the simplified thread changes.
 */
export const LiveTurnFoldContext = createContext(false);

export function useLiveTurnFoldShown(): boolean {
  return useContext(LiveTurnFoldContext);
}
