// Copyright (c) 2025 Reliant Labs

/**
 * hooks/provider-account-queries.ts in the forge demo's bundle (a demo twin,
 * vite-plugin-demo-bundle.ts): provider accounts are a Reliant account's, and
 * the demo has none, so the LLM usage view says they are unavailable, and the
 * chat's usage chip and usage-limit note find no account to show.
 */

import type * as Real from "./provider-account-queries";

export const useProviderAccounts: typeof Real.useProviderAccounts = () => ({ kind: "unavailable" });

export const useProviderAccountsView: typeof Real.useProviderAccountsView = () => ({ status: "unavailable" });
