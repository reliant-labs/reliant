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
    // Backend reports no usable provider. A live session is not a credential.
    getProviders.mockResolvedValue([providerStatus({})]);

    await useApiKeySetupStore.getState().checkApiKeys();

    expect(useApiKeySetupStore.getState().hasApiKey).toBe(false);
    expect(useApiKeySetupStore.getState().showModal).toBe(true);
    expect(openModal).toHaveBeenCalledWith("api-key-setup");
  });

  // REGRESSION: signing in used to mint a managed Reliant key on the spot,
  // via a self-heal in loadProviderCredentials whose two conditions ("signed
  // in", "no provider credentials") are exactly a brand-new account on the
  // onboarding model step. checkApiKeys runs from AuthInitializer on every
  // sign-in, so a user who never entered a coupon or card ended up with the
  // Reliant provider enabled and then hit the LLM proxy's wallet gate on their
  // first message. Granting a purchased entitlement belongs at an explicit
  // commit point (commitLaunchPlan.grantAiAccess), never here.
  it("never provisions a managed key for a signed-in user with no credentials", async () => {
    authState = signedIn;
    getProviders.mockResolvedValue([providerStatus({})]);

    await useApiKeySetupStore.getState().checkApiKeys();

    expect(provisionManagedKey).not.toHaveBeenCalled();
    // Exactly one read, because there is no mint-then-recheck round trip.
    expect(getProviders).toHaveBeenCalledTimes(1);
    expect(useApiKeySetupStore.getState().hasApiKey).toBe(false);
    // The setup modal IS the correct answer now: with no free tier, a user
    // holding no credentials genuinely has none.
    expect(useApiKeySetupStore.getState().showModal).toBe(true);
    expect(openModal).toHaveBeenCalledWith("api-key-setup");
  });

  it("reports a synced managed key as configured without prompting", async () => {
    authState = signedIn;
    // The key exists because something authorized it — a completed checkout,
    // a redeemed coupon, or the explicit enable-Reliant button in settings.
    getProviders.mockResolvedValue([providerStatus({ configured: true })]);

    await useApiKeySetupStore.getState().checkApiKeys();

    expect(provisionManagedKey).not.toHaveBeenCalled();
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