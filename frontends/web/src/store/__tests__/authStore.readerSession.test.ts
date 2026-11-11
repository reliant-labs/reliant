import { beforeEach, expect, it, vi } from "vitest";
import { act, fireEvent, screen } from "@testing-library/react";
import { renderBrowser, PROD, callsOf } from "@/components/Forge/Storage/__tests__/storageTestUtils";
import { createStorageFixtureApi } from "@/components/Forge/Storage/__fixtures__/fixtureStorageApi";
import { renderBrowser as renderDataBrowser } from "@/components/Forge/Data/__tests__/dataTestUtils";
import { createDataFixtureApi } from "@/components/Forge/Data/__fixtures__/fixtureDataApi";
vi.mock("@tanstack/react-router", async (original) => ({ ...(await original<typeof import("@tanstack/react-router")>()), useNavigate: () => vi.fn() }));
import type { Session, User } from "@supabase/supabase-js";
const auth = vi.hoisted(() => ({ setSession: vi.fn(), callback: undefined as undefined | ((event: string, session: Session | null) => void) }));
vi.mock("@/lib/supabase", () => ({
  supabase: { auth: {
    getSession: async () => ({ data: { session: null } }),
    setSession: auth.setSession,
    signOut: async () => ({ error: null }),
    onAuthStateChange: (callback: typeof auth.callback) => { auth.callback = callback; return { data: { subscription: { unsubscribe() {} } } }; },
  } }, setAuthStorageUnreadableHandler: vi.fn(),
}));
vi.mock("@/lib/logger", () => ({ logger: { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn() } }));
vi.mock("@/lib/telemetry", () => ({ setTelemetryUserId: vi.fn() }));
vi.mock("@/lib/analytics", () => ({ trackEvent: vi.fn() }));
vi.mock("@/lib/oauth-signin", () => ({ startOAuthSignIn: vi.fn(), oauthCallbackTransport: () => "browser" }));
vi.mock("../chatStore", () => ({ useChatStore: { getState: () => ({ reset() {} }) } }));
vi.mock("../projectStore", () => ({ useProjectStore: { getState: () => ({ reset() {} }) } }));
vi.mock("../worktreeStore", () => ({ useWorktreeStore: { getState: () => ({ reset() {} }) } }));
vi.mock("../chatNavigationStore", () => ({ useChatNavigationStore: { getState: () => ({ reset() {} }) } }));
vi.mock("../attachmentStore", () => ({ useAttachmentStore: { getState: () => ({ reset() {} }) } }));
vi.mock("../../hooks/worktree-queries", () => ({ resetWorktreeAnnouncements() {} }));
vi.mock("@/services/controlPlane/onboarding", () => ({ resetUserCache() {} }));
import { useAuthStore } from "../authStore";
import { getReaderSession } from "@/components/Forge/AuditedAccess/readerSession";
import { readAccess, writeAccess } from "@/components/Forge/AuditedAccess/accessSession-model";
const user = (id: string) => ({ id, is_anonymous: false }) as User;
beforeEach(() => {
  window.localStorage.clear(); window.sessionStorage.clear();
  useAuthStore.setState({ user: null, session: null, initialized: false });
});
it("synchronously isolates explicit signOut then B, and adoptSession without signOut", async () => {
  useAuthStore.setState({ user: user("A"), session: { user: user("A"), access_token: "A-token" } as Session });
  writeAccess("storage", "prod", "A reason");
  const a = getReaderSession();
  await useAuthStore.getState().signOut();
  expect(getReaderSession().principalId).toBeNull();
  useAuthStore.setState({ user: user("B"), session: { user: user("B"), access_token: "B-token" } as Session });
  expect(readAccess("storage", "prod")).toBeNull();
  expect(getReaderSession().epoch).not.toBe(a.epoch);
  writeAccess("data", "prod", "B rows");
  const b = getReaderSession();
  auth.setSession.mockResolvedValue({ data: { user: user("C"), session: { user: user("C"), access_token: "not-in-keys" } }, error: null });
  await useAuthStore.getState().adoptSession({ accessToken: "not-in-keys", refreshToken: "not-in-keys" });
  expect(getReaderSession().principalId).toBe("C");
  expect(getReaderSession().epoch).not.toBe(b.epoch);
  expect(readAccess("data", "prod")).toBeNull();
});
it.each(["signOut", "adoptSession", "authEvent"])("real auth boundary resets mounted Storage for %s", async (path) => {
  if (path === "authEvent") await useAuthStore.getState().initialize();
  useAuthStore.setState({ user: user("A"), session: { user: user("A"), access_token: "A-token" } as Session });
  const api = createStorageFixtureApi();
  renderBrowser(api, { bucket: "uploads", object: "docs/readme.txt" }, PROD);
  await screen.findByRole("dialog");
  fireEvent.change(screen.getByTestId("storage-access-reason"), { target: { value: "A reason" } });
  fireEvent.click(screen.getByTestId("storage-access-confirm"));
  await screen.findByTestId("storage-preview-text");
  const previous = callsOf(api, "listObjects").length;
  await act(async () => {
    if (path === "signOut") {
      await useAuthStore.getState().signOut();
      useAuthStore.setState({ user: user("B"), session: { user: user("B"), access_token: "B-token" } as Session });
    } else if (path === "adoptSession") {
      auth.setSession.mockResolvedValue({ data: { user: user("B"), session: { user: user("B"), access_token: "B-token" } }, error: null });
      await useAuthStore.getState().adoptSession({ accessToken: "B-token", refreshToken: "B-refresh" });
    } else auth.callback!("SIGNED_IN", { user: user("B") } as Session);
  });
  await screen.findByRole("dialog");
  expect(screen.queryByTestId("storage-preview-text")).not.toBeInTheDocument();
  expect(callsOf(api, "listObjects")).toHaveLength(previous);
  fireEvent.change(screen.getByTestId("storage-access-reason"), { target: { value: "B reason" } });
  fireEvent.click(screen.getByTestId("storage-access-confirm"));
  await screen.findByTestId("storage-preview-text");
  expect(callsOf(api, "listObjects").at(-1)).toMatchObject({ openBucket: true, access: { reason: "B reason", confirmEnvId: PROD.id } });
});

it.each(["signOut", "adoptSession", "authEvent"])("real auth boundary resets mounted Data for %s", async (path) => {
  if (path === "authEvent") await useAuthStore.getState().initialize();
  useAuthStore.setState({ user: user("A"), session: { user: user("A"), access_token: "A-token" } as Session });
  const api = createDataFixtureApi({ onlyShop: true });
  renderDataBrowser(api, { db: "shop", table: "public.users" }, PROD);
  await screen.findByRole("dialog");
  fireEvent.change(screen.getByTestId("data-access-reason"), { target: { value: "A reason" } });
  fireEvent.click(screen.getByTestId("data-access-confirm"));
  await screen.findAllByTestId("data-row");
  const previous = api.calls.filter((call) => call.method === "browseTable").length;
  await act(async () => {
    if (path === "signOut") {
      await useAuthStore.getState().signOut();
      useAuthStore.setState({ user: user("B"), session: { user: user("B"), access_token: "B-token" } as Session });
    } else if (path === "adoptSession") {
      auth.setSession.mockResolvedValue({ data: { user: user("B"), session: { user: user("B"), access_token: "B-token" } }, error: null });
      await useAuthStore.getState().adoptSession({ accessToken: "B-token", refreshToken: "B-refresh" });
    } else auth.callback!("SIGNED_IN", { user: user("B") } as Session);
  });
  await screen.findByRole("dialog");
  expect(screen.queryAllByTestId("data-row")).toHaveLength(0);
  expect(api.calls.filter((call) => call.method === "browseTable")).toHaveLength(previous);
  fireEvent.change(screen.getByTestId("data-access-reason"), { target: { value: "B reason" } });
  fireEvent.click(screen.getByTestId("data-access-confirm"));
  await screen.findAllByTestId("data-row");
  expect(api.calls.filter((call) => call.method === "browseTable").at(-1)?.req).toMatchObject({ access: { reason: "B reason", confirmEnvId: PROD.id } });
});

it("handles auth-event replacement, same-user token refresh, anonymous refusal and API-key replacement", async () => {
  await useAuthStore.getState().initialize();
  auth.callback!("SIGNED_IN", { user: user("A") } as Session);
  const a = getReaderSession();
  auth.callback!("TOKEN_REFRESHED", { user: user("A"), access_token: "rotated" } as Session);
  expect(getReaderSession()).toBe(a);
  auth.callback!("SIGNED_IN", { user: user("B") } as Session);
  expect(getReaderSession().principalId).toBe("B");
  expect(getReaderSession()).not.toBe(a);
  useAuthStore.getState().setUser({ ...user("anonymous"), is_anonymous: true });
  expect(getReaderSession().principalId).toBeNull();
  useAuthStore.getState().setSession({ user: user("session-only") } as Session);
  expect(getReaderSession().principalId).toBeNull();
  useAuthStore.getState().setUser(user("session-only"));
  expect(getReaderSession().principalId).toBe("session-only");
  useAuthStore.getState().setApiKeySession("one");
  const first = getReaderSession();
  useAuthStore.getState().setApiKeySession("two");
  expect(getReaderSession()).not.toBe(first);
  expect(JSON.stringify(getReaderSession())).not.toContain("two");
});
