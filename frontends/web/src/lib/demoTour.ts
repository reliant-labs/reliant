// Copyright (c) 2025 Reliant Labs

/**
 * How long the forge tour is on the public demo host (lib/shell.ts `demo`):
 * runtime config DEMO_TOUR. Owner default (2026-11-05) `short`: at most ten
 * steps, an auto-started tour having to earn every step. `full` is every
 * forge step. The Reliant and forge shells are not affected.
 */

import { loadConfig } from "./config_gen";
import { isDemoShell } from "./shell";

export type DemoTour = "short" | "full";

export function demoTour(): DemoTour {
  try {
    return loadConfig().DEMO_TOUR === "full" ? "full" : "short";
  } catch {
    return "short";
  }
}

/** On the demo host. */
export const onDemo = (): boolean => isDemoShell();

/** On the demo host with the full tour. */
export const onDemoFull = (): boolean => isDemoShell() && demoTour() === "full";

/** Everywhere but the demo host's short tour: the steps the short tour leaves out. */
export const notOnShortDemo = (): boolean => !isDemoShell() || demoTour() === "full";
