// Copyright (c) 2025 Reliant Labs

/**
 * hooks/provider-account-queries.ts in the forge demo's bundle (a demo twin,
 * vite-plugin-demo-bundle.ts): provider accounts are a Reliant account's, and
 * the demo has none, so the LLM usage view says they are unavailable.
 */

import type * as Real from "./provider-account-queries";

export const useProviderAccountsView: typeof Real.useProviderAccountsView = () => ({ status: "unavailable" });
