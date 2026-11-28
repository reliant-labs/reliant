// Copyright (c) 2025 Reliant Labs

/**
 * hooks/sidebar-project-queries.ts in the forge demo's bundle (a demo twin,
 * vite-plugin-demo-bundle.ts): nobody signs in to the demo, so there are no
 * account settings to hold pins or hidden projects. The choices are the
 * defaults and cannot be changed, and nothing reads the session owner or the
 * settings API, which the demo bundle must not contain.
 */

import { DEFAULT_SIDEBAR_PROJECT_PREFS } from "../lib/sidebarProjectPrefs";

import type * as Real from "./sidebar-project-queries";

export type SidebarProjectPrefsView = Real.SidebarProjectPrefsView;

const view: SidebarProjectPrefsView = Object.freeze({
  prefs: DEFAULT_SIDEBAR_PROJECT_PREFS,
  writable: false,
  loadFailed: false,
  retry: () => {},
  setPinned: () => {},
  setHidden: () => {},
  setShowAll: () => {},
  setFilter: () => {},
});

export const useSidebarProjectPrefs: typeof Real.useSidebarProjectPrefs = () => view;

/** No account settings in the demo: chat navigation (store/chatNavigationStore.ts) reads the defaults. */
export const cachedSidebarProjectSettings: typeof Real.cachedSidebarProjectSettings = () => ({});
