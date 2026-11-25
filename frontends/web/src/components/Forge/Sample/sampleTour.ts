// Copyright (c) 2026 Reliant Labs

/**
 * Whether the forge tour is walking the sample app right now
 * (sampleApp.ts). Memory only: a reload, or another tab, starts without it,
 * until the tour's `?tour=` is read again.
 *
 * The forge journey sets it while it is live in the signed-in shell
 * (Onboarding/journeys/forge.tsx `onLiveChange`) and clears it when it stops,
 * however it stops. Until then nothing shows the sample anywhere a person's
 * own apps are listed. What follows from it (the switcher's row, the sample's
 * cache cleared on the way down) is fixtures/scope.tsx's.
 *
 * Its own module, with nothing but the store, so the journey (which the demo
 * bundle carries too) sets it without importing the sample's scope.
 */

import { create } from "zustand";

export const useSampleTour = create<{ active: boolean }>(() => ({ active: false }));

export function setSampleTourActive(active: boolean): void {
  if (useSampleTour.getState().active !== active) useSampleTour.setState({ active });
}
