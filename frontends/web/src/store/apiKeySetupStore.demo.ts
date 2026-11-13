// Copyright (c) 2025 Reliant Labs

/**
 * store/apiKeySetupStore.ts in the forge demo's bundle (a demo twin,
 * vite-plugin-demo-bundle.ts). LLM keys are a Reliant account's; the demo has
 * no account and runs no agent, so there is never a key to ask for.
 */

import { create } from "zustand";

import type * as Real from "./apiKeySetupStore";

type ApiKeySetupState = ReturnType<typeof Real.useApiKeySetupStore.getState>;

export const useApiKeySetupStore: typeof Real.useApiKeySetupStore = create<ApiKeySetupState>()(() => ({
  showModal: false,
  isChecking: false,
  hasChecked: true,
  hasApiKey: true,
  checkApiKeys: async () => {},
  ensureApiKeyOrShowModal: async () => {},
  dismissModal: () => {},
  openModal: () => {},
  reset: () => {},
}));
