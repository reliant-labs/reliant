// Copyright (c) 2025 Reliant Labs

/**
 * The forge shell's half of the journey engine's contract (Onboarding/tourShell.ts):
 * once at boot, before anything renders the tour, say which shell this is and
 * keep progress in this origin's localStorage. The forge host may have nobody
 * signed in and has no settings service, so Reliant's settings-row storage
 * cannot hold it.
 */

import { localStorageTourStorage, setTourShell, setTourStorage } from "@/components/Onboarding/tourShell";

/** The localStorage entry holding the forge shell's tour progress. */
export const FORGE_TOUR_STORAGE_KEY = "forge.tour.v1";

export function bootForgeTour(): void {
  setTourShell("forge");
  setTourStorage(localStorageTourStorage(FORGE_TOUR_STORAGE_KEY));
}
