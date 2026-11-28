// Copyright (c) 2026 Reliant Labs

import { useAuthStore } from "../store/authStore";

/**
 * Whether the auth store holds a session: the gate an authenticated read
 * mounted on the sign-in screen waits on (the shell's pickers render before a
 * session lands), rather than going out without a credential and coming back
 * 401. Its own module so the demo bundle, where store/authStore is forbidden,
 * swaps it alone (useHasAuthSession.demo.ts) and keeps the reads behind it.
 */
export function useHasAuthSession(): boolean {
  return useAuthStore((state) => !!state.session);
}
