/**
 * Sign-out must not leak onboarding-checklist state into the next account.
 *
 * This is the confirmed trigger behind "the API-key modal appeared before the
 * onboarding screen for a brand-new user":
 *
 *   1. The checklist mirrors itself to localStorage (`reliant.checklist.*`) so
 *      a reload can decide offline whether the guide was dismissed. That
 *      mirror is device-wide, not per-user.
 *   2. `authStore.signOut()` resets seven stores — chat, project, worktree,
 *      navigation, attachment, tasks, process — but NOT the checklist, and it
 *      never clears the mirror.
 *   3. `apiKeySetupStore` defers the api-key-setup modal until
 *      `welcomeShown` is true, reading it as "this user has met the product".
 *
 * So a second user signing in on the same device inherits `welcomeShown: true`,
 * the deferral gate passes immediately, and with no credentials configured the
 * modal opens on top of onboarding.
 */
import { describe, expect, it, vi, beforeEach } from "vitest";
import { render } from "@testing-library/react";
import { waitFor } from "@testing-library/react";

const checklistReset = vi.fn();

vi.mock("@/store/onboardingChecklistStore", () => ({
  useOnboardingChecklistStore: { getState: () => ({ reset: checklistReset }) },
}));
vi.mock("../../store/onboardingChecklistStore", () => ({
  useOnboardingChecklistStore: { getState: () => ({ reset: checklistReset }) },
}));

let authState: Record<string, unknown>;
vi.mock("@/store/authStore", () => ({
  useAuthStore: Object.assign(
    (selector?: (s: unknown) => unknown) =>
      selector ? selector(authState) : authState,
    { getState: () => authState },
  ),
}));

const resetApiKeyCheck = vi.fn();
vi.mock("@/store/apiKeySetupStore", () => ({
  useApiKeySetupStore: (selector: (s: unknown) => unknown) =>
    selector({ checkApiKeys: vi.fn(), reset: resetApiKeyCheck }),
}));

vi.mock("@/store/globalDataStore", () => ({
  useGlobalDataStore: (selector: (s: unknown) => unknown) =>
    selector({ prefetch: vi.fn(async () => undefined) }),
}));
vi.mock("@/store/privacyStore", () => ({
  usePrivacyStore: (selector: (s: unknown) => unknown) =>
    selector({ initialize: vi.fn(async () => undefined) }),
}));
vi.mock("@/store/projectStore", () => ({
  useProjectStore: {
    getState: () => ({ loadProjects: vi.fn(async () => undefined) }),
  },
}));
vi.mock("@/lib/logger", () => ({
  logger: { info: vi.fn(), warn: vi.fn(), error: vi.fn() },
}));
vi.mock("@/lib/supabase", () => ({
  supabase: { auth: { getSession: vi.fn(async () => ({ data: { session: null } })) } },
}));
vi.mock("@/lib/sentry", () => ({ initSentry: vi.fn(async () => undefined) }));
vi.mock("@/lib/analytics", () => ({
  identifyUser: vi.fn(),
  resetUser: vi.fn(async () => undefined),
}));
vi.mock("@/services/settingsSync", () => ({
  settingsSync: {
    initialize: vi.fn(async () => undefined),
    applyAppearanceSettingsToDOM: vi.fn(),
  },
}));

import { AuthInitializer } from "../AuthInitializer";

const signedIn = {
  initialize: vi.fn(async () => undefined),
  user: { id: "user-1", email: "a@example.com" },
  session: { access_token: "t" },
  loading: false,
  initialized: true,
};

const signedOut = { ...signedIn, user: null, session: null };

describe("AuthInitializer checklist reset on sign-out", () => {
  beforeEach(() => {
    vi.clearAllMocks();
    vi.useFakeTimers();
  });

  it("resets the onboarding checklist when the user signs out", async () => {
    authState = signedIn;
    const { rerender } = render(
      <AuthInitializer>
        <div />
      </AuthInitializer>,
    );

    // Let the post-login prefetch effect run so `hasPrefetched` flips — that
    // ref is what marks "a session actually started here", and the logout
    // branch is gated on it.
    await vi.advanceTimersByTimeAsync(10);

    expect(checklistReset).not.toHaveBeenCalled();

    authState = signedOut;
    rerender(
      <AuthInitializer>
        <div />
      </AuthInitializer>,
    );

    vi.useRealTimers();
    await waitFor(() => expect(checklistReset).toHaveBeenCalledTimes(1));
  });
});
