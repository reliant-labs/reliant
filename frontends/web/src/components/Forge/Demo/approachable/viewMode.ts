// Copyright (c) 2026 Reliant Labs

/**
 * The public demo's two views of the same console:
 *
 *   simple    (the default) five nav rows in everyday words — Home, What's
 *             happening, Releases, People, Agents — over the same pages, each
 *             plain name with the console's term beside it ("Slow requests
 *             (traces)", "Recordings (sessions)"). Traces and recordings stay
 *             first-class: a slow request's timeline shows inline, and a
 *             recording plays when opened. No page card opens by itself.
 *   advanced  the full console nav, exactly as the product has it.
 *
 * Remembered for this browser (localStorage), read through
 * useSyncExternalStore so the nav, the page titles and the toggle agree.
 */

import { useSyncExternalStore } from "react";

export type DemoViewMode = "simple" | "advanced";

const KEY = "forge-demo.view";
const listeners = new Set<() => void>();

function read(): DemoViewMode {
  try {
    return localStorage.getItem(KEY) === "advanced" ? "advanced" : "simple";
  } catch {
    return "simple";
  }
}

let current: DemoViewMode | null = null;

export function demoViewMode(): DemoViewMode {
  return (current ??= read());
}

export function setDemoViewMode(mode: DemoViewMode): void {
  current = mode;
  try {
    localStorage.setItem(KEY, mode);
  } catch {
    // Private mode: the choice lasts for the visit.
  }
  listeners.forEach((listener) => listener());
}

function subscribe(listener: () => void): () => void {
  listeners.add(listener);
  return () => listeners.delete(listener);
}

export function useDemoViewMode(): DemoViewMode {
  return useSyncExternalStore(subscribe, demoViewMode, () => "simple");
}
