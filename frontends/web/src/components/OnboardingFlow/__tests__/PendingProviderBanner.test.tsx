import { fireEvent, render, screen, waitFor } from "@testing-library/react";
import { loadChanges } from "@reliantlabs/forge-web-runtime";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import type { AuthIdentity } from "@/lib/auth/identity";
import { setChangesStoreForTests } from "@/lib/changesStore";

const mocks = vi.hoisted(() => ({
  readPendingProvider: vi.fn(),
  clearPendingProvider: vi.fn(),
  statuses: { data: { providers: [] as { configured: boolean }[] } } as { data?: { providers: { configured: boolean }[] } },
  openModal: vi.fn(),
  identity: null as AuthIdentity | null,
}));

vi.mock("@/lib/pendingProvider", () => ({
  readPendingProvider: () => mocks.readPendingProvider(),
  clearPendingProvider: () => mocks.clearPendingProvider(),
}));
vi.mock("@/hooks/settings-queries", async (importOriginal) => ({
  ...(await importOriginal<typeof import("@/hooks/settings-queries")>()),
  useProviderStatuses: () => mocks.statuses,
}));
vi.mock("@/store/modalStore", () => ({
  useModalStore: (select: (s: { openModal: typeof mocks.openModal }) => unknown) => select({ openModal: mocks.openModal }),
}));
vi.mock("@/lib/analytics", () => ({ trackEvent: vi.fn() }));
vi.mock("@/lib/auth/use-auth", () => ({ useAuthIdentity: () => mocks.identity }));

import { PendingProviderBanner } from "../PendingProviderBanner";

beforeEach(() => {
  vi.clearAllMocks();
  mocks.clearPendingProvider.mockResolvedValue(undefined);
  mocks.readPendingProvider.mockResolvedValue("claude");
  mocks.statuses = { data: { providers: [] } };
  mocks.identity = person("u_1");
  setChangesStoreForTests(null);
  vi.stubGlobal("fetch", vi.fn(async () => new Response("", { status: 404 })));
});

afterEach(() => {
  vi.unstubAllGlobals();
  setChangesStoreForTests(null);
});

function person(id: string, over: Partial<AuthIdentity> = {}): AuthIdentity {
  return {
    id,
    email: `${id}@example.com`,
    name: null,
    avatarUrl: null,
    isAnonymous: false,
    emailVerified: true,
    createdAtMs: null,
    isApiKey: false,
    provider: "supabase",
    ...over,
  };
}

/** A real store over a snapshot that rolls `pending-provider-copy` out to every user. */
async function rolledOutStore() {
  const snapshot = {
    schema: "forge.changes/v1",
    revision: 7,
    changes: {
      "pending-provider-copy": {
        variants: { current: false, "outcome-first": true },
        default_variant: "current",
        delivery: { mode: "rollout", unit: "user", run: "r_test1", salt: "r_test1", percent_bp: 10000, rollout_variant: "outcome-first" },
      },
    },
  };
  const store = await loadChanges({ fetch: async () => new Response(JSON.stringify(snapshot), { status: 200 }) });
  return { store, evaluate: vi.spyOn(store, "evaluate") };
}

describe("PendingProviderBanner — finish the sign-in the user chose in onboarding", () => {
  it("offers to connect the subscription they picked", async () => {
    render(<PendingProviderBanner />);

    expect(await screen.findByText(/connect your claude subscription/i)).toBeInTheDocument();
    expect(screen.getByText(/desktop app or cli/i)).toBeInTheDocument();
  });

  it("opens the sign-in dialog on that provider", async () => {
    render(<PendingProviderBanner />);
    fireEvent.click(await screen.findByRole("button", { name: /^connect claude subscription$/i }));

    expect(mocks.openModal).toHaveBeenCalledWith("api-key-setup", { provider: "claude" });
  });

  it("'Use an API key instead' opens the dialog on the Anthropic key", async () => {
    render(<PendingProviderBanner />);
    fireEvent.click(await screen.findByRole("button", { name: /use an api key instead/i }));

    expect(mocks.openModal).toHaveBeenCalledWith("api-key-setup", { provider: "anthropic" });
  });

  it("shows nothing when no provider was deferred", async () => {
    mocks.readPendingProvider.mockResolvedValue(null);
    render(<PendingProviderBanner />);

    await waitFor(() => expect(mocks.readPendingProvider).toHaveBeenCalled());
    expect(screen.queryByTestId("pending-provider-banner")).toBeNull();
  });

  it("goes away, and forgets the choice, once any provider is configured", async () => {
    mocks.statuses = { data: { providers: [{ configured: true }] } };
    render(<PendingProviderBanner />);

    await waitFor(() => expect(mocks.clearPendingProvider).toHaveBeenCalled());
    expect(screen.queryByTestId("pending-provider-banner")).toBeNull();
  });

  it("can be dismissed, and then stays dismissed", async () => {
    render(<PendingProviderBanner />);
    fireEvent.click(await screen.findByRole("button", { name: /dismiss/i }));

    expect(mocks.clearPendingProvider).toHaveBeenCalledTimes(1);
    expect(screen.queryByTestId("pending-provider-banner")).toBeNull();
  });
});

describe("PendingProviderBanner — the pending-provider-copy change", () => {
  it("waits for a delayed snapshot before exposing the selected variant", async () => {
    let resolve!: (response: Response) => void;
    vi.stubGlobal("fetch", vi.fn(() => new Promise<Response>((done) => { resolve = done; })));
    const { rerender } = render(<PendingProviderBanner />);
    await waitFor(() => expect(mocks.readPendingProvider).toHaveBeenCalled());
    expect(screen.queryByTestId("pending-provider-banner")).toBeNull();

    resolve(new Response(JSON.stringify({
      schema: "forge.changes/v1", revision: 7, changes: {
        "pending-provider-copy": {
          variants: { current: false, "outcome-first": true }, default_variant: "current",
          delivery: { mode: "rollout", unit: "user", run: "r_test1", salt: "r_test1", percent_bp: 10000, rollout_variant: "outcome-first" },
        },
      },
    }), { status: 200 }));
    expect(await screen.findByText(/connect a model to start chatting/i)).toBeInTheDocument();
    rerender(<PendingProviderBanner />);
    expect(screen.getByTestId("pending-provider-banner")).toHaveAttribute("data-change-variant", "outcome-first");
  });

  it("is today's banner when the snapshot is missing (every change at its default)", async () => {
    render(<PendingProviderBanner />);

    expect(await screen.findByText(/connect your claude subscription/i)).toBeInTheDocument();
    expect(screen.getByTestId("pending-provider-banner")).not.toHaveAttribute("data-change-variant");
  });

  it("is today's banner when the snapshot does not carry the change, which it evaluates as unknown", async () => {
    const store = await loadChanges({ fetch: async () => new Response(JSON.stringify({ schema: "forge.changes/v1", revision: 3, changes: {} }), { status: 200 }) });
    const evaluate = vi.spyOn(store, "evaluate");
    setChangesStoreForTests(store);
    render(<PendingProviderBanner />);

    expect(await screen.findByText(/connect your claude subscription/i)).toBeInTheDocument();
    expect(evaluate).toHaveReturnedWith(expect.objectContaining({ reason: "unknown" }));
  });

  it("shows outcome-first to a signed-in person the change selects, with the same actions", async () => {
    const { store, evaluate } = await rolledOutStore();
    setChangesStoreForTests(store);
    render(<PendingProviderBanner />);

    expect(await screen.findByText(/connect a model to start chatting/i)).toBeInTheDocument();
    expect(screen.getByTestId("pending-provider-banner")).toHaveAttribute("data-change-variant", "outcome-first");
    expect(evaluate).toHaveBeenCalledWith("pending-provider-copy", { targetingKey: "u_1" });

    fireEvent.click(screen.getByRole("button", { name: /^connect claude subscription$/i }));
    expect(mocks.openModal).toHaveBeenLastCalledWith("api-key-setup", { provider: "claude" });
    fireEvent.click(screen.getByRole("button", { name: /^use an api key$/i }));
    expect(mocks.openModal).toHaveBeenLastCalledWith("api-key-setup", { provider: "anthropic" });
  });

  it("enrolls no one who never sees the banner", async () => {
    mocks.readPendingProvider.mockResolvedValue(null);
    const { store, evaluate } = await rolledOutStore();
    setChangesStoreForTests(store);
    render(<PendingProviderBanner />);

    await waitFor(() => expect(mocks.readPendingProvider).toHaveBeenCalled());
    expect(evaluate).not.toHaveBeenCalled();
  });

  it.each([
    ["a guest", person("anon_1", { isAnonymous: true })],
    ["an API-key session", person("key_1", { isApiKey: true })],
  ])("gives %s today's banner: no unit, never enrolled", async (_label, identity) => {
    mocks.identity = identity;
    const { store, evaluate } = await rolledOutStore();
    setChangesStoreForTests(store);
    render(<PendingProviderBanner />);

    expect(await screen.findByText(/connect your claude subscription/i)).toBeInTheDocument();
    expect(evaluate).toHaveBeenCalledWith("pending-provider-copy", {});
    expect(evaluate).toHaveReturnedWith(expect.objectContaining({ reason: "no_unit" }));
  });

  it("keeps the variant it showed when the snapshot changes under it", async () => {
    const { store } = await rolledOutStore();
    setChangesStoreForTests(store);
    const { rerender } = render(<PendingProviderBanner />);
    expect(await screen.findByText(/connect a model to start chatting/i)).toBeInTheDocument();

    setChangesStoreForTests(null);
    rerender(<PendingProviderBanner />);
    expect(screen.getByText(/connect a model to start chatting/i)).toBeInTheDocument();
  });
});
