// Copyright (c) 2025 Reliant Labs

/**
 * api/authProvider.ts in the forge demo's bundle (a demo twin,
 * vite-plugin-demo-bundle.ts): nobody signs in to the demo, so there is never
 * a token, a session or a refresh, and no Supabase.
 */

import type * as Real from "./authProvider";

export type AuthTokenProvider = Real.AuthTokenProvider;

export const browserAuthTokenProvider: typeof Real.browserAuthTokenProvider = {
  getToken: async () => null,
  hasSession: async () => false,
  refresh: async () => null,
};

export const setAuthTokenProvider: typeof Real.setAuthTokenProvider = () => {};

export const getAuthTokenProvider: typeof Real.getAuthTokenProvider = () => browserAuthTokenProvider;
