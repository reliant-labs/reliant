// Copyright (c) 2025 Reliant Labs

/**
 * components/Layout/stuckLoadEscape.ts in the forge demo's bundle (a demo
 * twin, vite-plugin-demo-bundle.ts). The app's escape from a stalled load is
 * to sign out (store/authStore) and go to sign-in. Nobody is signed in to the
 * demo and it has no sign-in page, so the escape is a reload.
 */

import type * as Real from "./stuckLoadEscape";

export const stuckLoadEscape: typeof Real.stuckLoadEscape = {
  label: "Reload",
  busyLabel: "Reloading…",
  async run() {
    window.location.reload();
  },
};
