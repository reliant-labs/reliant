/**
 * forge sign-in: the session is the broker's HttpOnly cookie, so no access
 * token ever appears in supabase's storage. The authenticated startup must
 * run on the identity alone, not wait ten seconds for a token and give up.
 */
import { describe, expect, it, vi, beforeEach } from "vitest";
import { render, waitFor } from "@testing-library/react";

import { identityOf } from "@/lib/auth/identity";

let forge = true;
vi.mock("@/lib/auth/forge-api", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/lib/auth/forge-api")>()),
  isForgeAuth: () => forge,
  authProviderName: () => (forge ? "forge" : "supabase"),
}));

let authState: Record<string, unknown>;
vi.mock("@/store/authStore", () => ({
  useAuthStore: Object.assign(
    (selector?: (s: unknown) => unknown) => (selector ? selector(authState) : authState),
    { getState: () => authState },
  ),
}));

vi.mock("@/store/apiKeySetupStore", () => ({
  useApiKeySetupStore: (selector: (s: unknown) => unknown) => selector({ checkApiKeys: vi.fn(async () => undefined), reset: vi.fn() }),
}));
vi.mock("@/store/globalDataStore", () => ({
  useGlobalDataStore: (selector: (s: unknown) => unknown) => selector({ prefetch: vi.fn(async () => undefined) }),
}));
vi.mock("@/hooks/privacy-queries", () => ({ loadPrivacySettings: vi.fn(async () => undefined) }));
vi.mock("@/hooks/project-queries", () => ({ fetchProjects: vi.fn(async () => undefined) }));
vi.mock("@/store/onboardingChecklistStore", () => ({
  useOnboardingChecklistStore: { getState: () => ({ reset: vi.fn(), loadState: vi.fn(async () => undefined) }) },
}));
vi.mock("@/store/tourStore", () => ({ useTourStore: { getState: () => ({ loadState: vi.fn(async () => undefined) }) } }));
vi.mock("@/lib/telemetry", () => ({ initTelemetry: vi.fn(async () => undefined) }));
vi.mock("@/lib/logger", () => ({ logger: { info: vi.fn(), warn: vi.fn(), error: vi.fn() } }));
const getSession = vi.fn(async () => ({ data: { session: null } }));
vi.mock("@/lib/supabase", () => ({ supabase: { auth: { getSession: () => getSession() } } }));
const settingsInitialize = vi.fn(async () => undefined);
vi.mock("@/services/settingsSync", () => ({
  settingsSync: { initialize: () => settingsInitialize(), applyAppearanceSettingsToDOM: vi.fn() },
}));

import { AuthInitializer } from "../AuthInitializer";

const forgeUser = { id: "acct-1", email: "a@example.com", app_metadata: { provider: "forge" }, email_confirmed_at: "1970-01-01T00:00:00.000Z" };
const signedIn = {
  initialize: vi.fn(async () => undefined),
  signOut: vi.fn(async () => undefined),
  authError: null,
  user: forgeUser,
  identity: identityOf(forgeUser as never),
  // forge-session's store session: no token, the cookie is the credential.
  session: { access_token: "", user: forgeUser },
  loading: false,
  initialized: true,
};

describe("AuthInitializer under forge sign-in", () => {
  beforeEach(() => {
    vi.clearAllMocks();
  });

  it("runs the authenticated startup without waiting for a supabase token", async () => {
    forge = true;
    authState = signedIn;
    render(
      <AuthInitializer>
        <div />
      </AuthInitializer>,
    );
    await waitFor(() => expect(settingsInitialize).toHaveBeenCalledTimes(1), { timeout: 1000 });
    expect(getSession).not.toHaveBeenCalled();
  });

  it("still waits for the token under supabase sign-in", async () => {
    forge = false;
    authState = { ...signedIn, user: { id: "u1" }, identity: identityOf({ id: "u1" } as never) };
    getSession.mockResolvedValue({ data: { session: { access_token: "t" } } } as never);
    render(
      <AuthInitializer>
        <div />
      </AuthInitializer>,
    );
    await waitFor(() => expect(settingsInitialize).toHaveBeenCalledTimes(1), { timeout: 1000 });
    expect(getSession).toHaveBeenCalled();
  });
});
