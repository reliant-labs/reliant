import { afterAll, beforeEach, describe, expect, it, vi } from "vitest";

// --- Mocks -----------------------------------------------------------------
// The store reaches into the api client, the auth/modal/checklist stores, and
// (lazily) the onboarding service. We mock each so the tests exercise only the
// credential-gating logic.

const getProviders = vi.fn();
vi.mock("../../api/client", () => ({
  api: { settings: { getProviders: () => getProviders() } },
}));

const provisionManagedKey = vi.fn();
vi.mock("../../services/controlPlane/onboarding", () => ({
  onboardingService: { provisionManagedKey: () => provisionManagedKey() },
}));

let authState: { user: unknown; session: unknown };
vi.mock("../authStore", () => ({
  useAuthStore: { getState: () => authState },
}));

let activeModal: string | null;
const openModal = vi.fn((id: string) => {
  activeModal = id;
});
const closeModal = vi.fn(() => {
  activeModal = null;
});
vi.mock("../modalStore", () => ({
  useModalStore: {
    getState: () => ({ activeModal, openModal, closeModal }),
  },
}));

vi.mock("../onboardingChecklistStore", () => ({
  useOnboardingChecklistStore: {
    getState: () => ({ isInitialized: true, welcomeShown: true }),
  },
}));

vi.mock("../../lib/logger", () => ({
  logger: { info: vi.fn(), warn: vi.fn(), error: vi.fn() },
}));

// The store reads/writes localStorage for the "permanently dismissed" flag.
// The test runtime's localStorage stub lacks a working clear(), so back it with
// a plain Map.
const storage = new Map<string, string>();
vi.stubGlobal("localStorage", {
  getItem: (k: string) => storage.get(k) ?? null,
  setItem: (k: string, v: string) => storage.set(k, v),
  removeItem: (k: string) => storage.delete(k),
  clear: () => storage.clear(),
});

import { useApiKeySetupStore } from "../apiKeySetupStore";

// Don't let the localStorage stub bleed into sibling test files sharing the run.
afterAll(() => {
  vi.unstubAllGlobals();
});

const signedIn = { user: { id: "u1" }, session: { token: "t" } };
const signedOut = { user: null, session: null };

function providerStatus(overrides: Partial<{
  provider: string;
  hasApiKey: boolean;
  configured: boolean;
}>) {
  return { provider: "reliant", hasApiKey: false, configured: false, ...overrides };
}

describe("apiKeySetupStore credential gating", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    localStorage.clear();
    activeModal = null;
    authState = signedOut;
    // reset() clears the per-session self-heal guard and cached state.
    useApiKeySetupStore.getState().reset();
  });

  it("shows the setup modal for a signed-in user with no synced key (no session bypass)", async () => {
    authState = signedIn;
    // Backend reports no usable provider, and self-heal cannot mint a key.
    getProviders.mockResolvedValue([providerStatus({})]);
    provisionManagedKey.mockResolvedValue({ synced: false });

    await useApiKeySetupStore.getState().checkApiKeys();

    expect(useApiKeySetupStore.getState().hasApiKey).toBe(false);
    expect(useApiKeySetupStore.getState().showModal).toBe(true);
    expect(openModal).toHaveBeenCalledWith("api-key-setup");
  });

  it("self-heals the managed Reliant key and suppresses the modal", async () => {
    authState = signedIn;
    // First fetch: nothing configured. Self-heal succeeds. Re-fetch: configured.
    getProviders
      .mockResolvedValueOnce([providerStatus({})])
      .mockResolvedValueOnce([providerStatus({ configured: true })]);
    provisionManagedKey.mockResolvedValue({ synced: true });

    await useApiKeySetupStore.getState().checkApiKeys();

    expect(provisionManagedKey).toHaveBeenCalledTimes(1);
    expect(getProviders).toHaveBeenCalledTimes(2);
    expect(useApiKeySetupStore.getState().hasApiKey).toBe(true);
    expect(useApiKeySetupStore.getState().showModal).toBe(false);
    expect(openModal).not.toHaveBeenCalled();
  });

  it("does not attempt self-heal when signed out", async () => {
    authState = signedOut;
    getProviders.mockResolvedValue([providerStatus({})]);

    await useApiKeySetupStore.getState().checkApiKeys();

    expect(provisionManagedKey).not.toHaveBeenCalled();
    expect(useApiKeySetupStore.getState().showModal).toBe(true);
  });

  it("treats an existing manual key as configured without self-healing", async () => {
    authState = signedIn;
    getProviders.mockResolvedValue([
      providerStatus({ provider: "openai", hasApiKey: true }),
    ]);

    await useApiKeySetupStore.getState().checkApiKeys();

    expect(provisionManagedKey).not.toHaveBeenCalled();
    expect(useApiKeySetupStore.getState().hasApiKey).toBe(true);
    expect(useApiKeySetupStore.getState().showModal).toBe(false);
  });
});