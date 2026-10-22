import { beforeEach, describe, expect, it, vi } from "vitest";

// ---------------------------------------------------------------------------
// An unreadable stored session must become a REAL auth state.
//
// This is the last link in the "getToken() returned null while signed in"
// chain. Once the storage adapter reports that the saved blob is permanently
// unreadable, the store still holds a user from an earlier onAuthStateChange —
// so the UI keeps rendering as signed in while `getToken()` resolves null and
// every RPC goes out with no Authorization header, drawing
// "missing authorization token" from the server's empty-header branch
// (internal/grpc/interceptors/auth.go:182).
//
// Dropping the stale in-memory session is what converts that invisible
// half-authenticated state into the honest one: signed out, with a reason.
// ---------------------------------------------------------------------------

const mocks = vi.hoisted(() => ({
  getSession: vi.fn(),
  onAuthStateChange: vi.fn(),
  setAuthStorageUnreadableHandler: vi.fn(),
}));

vi.mock("@/lib/supabase", () => ({
  supabase: {
    auth: {
      getSession: mocks.getSession,
      onAuthStateChange: mocks.onAuthStateChange,
    },
  },
  setAuthStorageUnreadableHandler: mocks.setAuthStorageUnreadableHandler,
}));

import { useAuthStore } from "../authStore";

const signedInSession = {
  access_token: "stored-access-token",
  refresh_token: "stored-refresh-token",
  user: { id: "e08d19f2-50b1-4e2e-babd-d78ac2f49269", email: "user@example.com" },
};

/** Runs initialize() and returns the handler it registered. */
async function initializeAndCaptureHandler(): Promise<() => void> {
  await useAuthStore.getState().initialize();

  const registered = mocks.setAuthStorageUnreadableHandler.mock.calls.at(-1)?.[0];
  expect(
    registered,
    "initialize() must register an unreadable-storage handler",
  ).toBeTypeOf("function");

  return registered as () => void;
}

beforeEach(() => {
  vi.clearAllMocks();
  mocks.getSession.mockResolvedValue({ data: { session: null } });
  mocks.onAuthStateChange.mockReturnValue({
    data: { subscription: { unsubscribe: vi.fn() } },
  });

  useAuthStore.setState({
    user: null,
    session: null,
    loading: false,
    initialized: false,
    authError: null,
  });
  localStorage.removeItem("reliant-api-key");
});

describe("authStore: permanently unreadable stored session", () => {
  it("registers a handler during initialize", async () => {
    await initializeAndCaptureHandler();
  });

  it("drops the in-memory session so the app stops looking signed in", async () => {
    // Boot with a live session, exactly as a running app would hold.
    mocks.getSession.mockResolvedValue({
      data: { session: signedInSession },
    });

    const onUnreadable = await initializeAndCaptureHandler();
    expect(useAuthStore.getState().user).not.toBeNull();

    onUnreadable();

    // This is the half-authenticated state being closed: without it the store
    // keeps a user that no token can ever be produced for.
    expect(useAuthStore.getState().user).toBeNull();
    expect(useAuthStore.getState().session).toBeNull();
  });

  it("explains why the user was signed out", async () => {
    const onUnreadable = await initializeAndCaptureHandler();

    onUnreadable();

    const { authError } = useAuthStore.getState();
    expect(authError).toBeTruthy();
    // Surfaced through AccountSettings' existing authError channel, so the
    // sign-out reads as an explained event rather than an unexplained bounce.
    expect(authError).toMatch(/sign in again/i);
  });

  it("leaves the app in a settled, non-loading state", async () => {
    const onUnreadable = await initializeAndCaptureHandler();

    onUnreadable();

    // AuthGuard renders nothing while `loading` or `!initialized`, so a stuck
    // flag here would replace the sign-in screen with a blank window.
    expect(useAuthStore.getState().loading).toBe(false);
    expect(useAuthStore.getState().initialized).toBe(true);
  });
});
