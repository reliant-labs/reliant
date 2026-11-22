// Copyright (c) 2025 Reliant Labs

/**
 * hooks/settings-queries.ts in the forge demo's bundle (a demo twin,
 * vite-plugin-demo-bundle.ts): nobody signs in to the demo, so there are no
 * saved keyboard shortcuts (store/shortcutsStore uses its defaults) and
 * nothing to save them to. The real module reaches the settings API and
 * store/authStore, which the demo bundle must not contain.
 */

import type * as Real from "./settings-queries";

export const fetchShortcuts: typeof Real.fetchShortcuts = async () => "";

export const updateShortcuts: typeof Real.updateShortcuts = async () => {
  throw new Error("Shortcuts are saved to your Reliant account, not in the forge demo.");
};
