// Copyright (c) 2025 Reliant Labs

/**
 * lib/auth/accountScopedState.ts in the forge demo's bundle (a demo twin,
 * vite-plugin-demo-bundle.ts). Caches register a reset here so the auth store
 * can empty them when the signed-in account changes. Nobody signs in to the
 * demo, so the account never changes and no reset ever runs; lib/auth is
 * sign-in code the demo bundle must not contain.
 */

import type * as Real from "./accountScopedState";

export const onAccountChange: typeof Real.onAccountChange = () => () => false;
