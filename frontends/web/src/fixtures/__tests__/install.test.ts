/**
 * Fixture mode end to end, in the page: installFixtures puts the shop app
 * pack behind the app's own clients and interceptors, read-only, on the
 * harness's clock, and the probe records what the harness fails on.
 */
import { createRouterTransport, createClient, Code, ConnectError } from "@connectrpc/connect";
import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { ChatService } from "@/gen/reliant/v1/chat_pb";
import { DaemonRegistryService } from "@/gen/reliant/v1/daemon_registry_pb";
import { ProjectService } from "@/gen/reliant/v1/project_pb";
import { SettingsService } from "@/gen/reliant/v1/settings_pb";
import { connectClient, installFixtureTransport } from "@/lib/connect";
import { setRuntimeConfig } from "@/test/runtimeConfig";

import { countingInterceptor, installFixtures, recordingInterceptor, resolveOverlays, type ForgeFixturesProbe } from "../install";
import { SHOP_READ_ONLY_MESSAGE } from "../shop/index";
import { demoNow, resetDemoNowForTests } from "../shop/shopStory";
import { CHAT_IDS, PROJECT_IDS } from "../shop/workspace";

vi.mock("@/lib/logger", () => ({
  logger: { info: vi.fn(), warn: vi.fn(), error: vi.fn(), debug: vi.fn() },
}));

const FIXED_NOW = Date.UTC(2026, 10, 18, 15, 30);
const probe = () => window.__forgeFixtures as ForgeFixturesProbe;

let realFetch: typeof window.fetch;
beforeEach(() => {
  realFetch = window.fetch;
  resetDemoNowForTests();
});
afterEach(() => {
  installFixtureTransport(null);
  window.fetch = realFetch;
  delete window.__forgeFixtures;
  localStorage.clear();
});

describe("resolveOverlays", () => {
  const registry = { "no-daemons": () => ({ name: "no-daemons" }), workflows: () => ({ name: "workflows" }) };
  it("keeps registered names, in order, once each, and reports the rest", () => {
    const warn = vi.fn();
    expect(resolveOverlays(["workflows", "nope", "no-daemons", "workflows"], registry, warn)).toEqual(["workflows", "no-daemons"]);
    expect(warn).toHaveBeenCalledOnce();
    expect(warn.mock.calls[0][0]).toMatch(/no overlay named "nope"/);
  });
});

describe("the probe's interceptors", () => {
  it("counts a call while it is in flight, and records a refused write and an unanswered read", async () => {
    const p = { started: 0, inflight: 0, rpcs: {}, refused: [] as string[], unknown: [] as string[] };
    let release!: () => void;
    let seenInflight = -1;
    const transport = createRouterTransport(
      (router) =>
        router.service(SettingsService, {
          listSettings: async () => {
            seenInflight = p.inflight;
            await new Promise<void>((resolve) => (release = resolve));
            return {};
          },
          getSetting: () => {
            throw new ConnectError(SHOP_READ_ONLY_MESSAGE, Code.PermissionDenied);
          },
          getPrivacySettings: () => {
            throw new ConnectError("fixture-transport: no route, scenario handler or service default for x", Code.Unimplemented);
          },
        }),
      { transport: { interceptors: [countingInterceptor(p), recordingInterceptor(p, SHOP_READ_ONLY_MESSAGE)] } },
    );
    const client = createClient(SettingsService, transport);
    const pending = client.listSettings({});
    await vi.waitFor(() => expect(seenInflight).toBe(1));
    release();
    await pending;
    expect(p.inflight).toBe(0);
    await expect(client.getSetting({ key: "k" })).rejects.toThrow();
    await expect(client.getPrivacySettings({})).rejects.toThrow();
    expect(p).toMatchObject({ started: 3, inflight: 0, refused: ["reliant.v1.SettingsService/GetSetting"], unknown: ["reliant.v1.SettingsService/GetPrivacySettings"] });
  });
});

describe("installFixtures", () => {
  it("serves the shop app pack through the app's own clients, on the harness's clock", async () => {
    setRuntimeConfig({ FIXTURES_SCENARIO: "shop" });
    window.__forgeFixtures = { now: FIXED_NOW };
    await installFixtures({ scenario: "shop", readOnly: true, overlays: [] });

    expect(probe()).toMatchObject({ ready: true, scenario: "shop", readOnly: true, overlays: [] });
    expect(demoNow()).toBe(FIXED_NOW);
    const { project } = await connectClient(ProjectService).getProject({ projectId: PROJECT_IDS.shop });
    expect(project?.id).toBe(PROJECT_IDS.shop);
    expect((await connectClient(DaemonRegistryService).listDaemons({})).daemons.length).toBeGreaterThan(0);
    expect(probe().rpcs["reliant.v1.ProjectService/GetProject"]).toBe(1);
    expect(probe().inflight).toBe(0);
    // ListDaemons is proto-annotated; GetProject is one of the pack's named `answers`.
    expect(probe().unannotated).not.toContain("reliant.v1.DaemonRegistryService/ListDaemons");
    expect(probe().unannotated).not.toContain("reliant.v1.ProjectService/GetProject");
  });

  it("refuses a write and records it", async () => {
    await installFixtures({ scenario: "shop", readOnly: true, overlays: [] });
    await expect(connectClient(ChatService).deleteChat({ chatId: CHAT_IDS.cartTotals })).rejects.toThrow();
    expect(probe().refused).toEqual(["reliant.v1.ChatService/DeleteChat"]);
  });

  it("applies an allow-listed overlay, from the harness or the URL's ?fixtures=", async () => {
    window.__forgeFixtures = { overlays: ["no-daemons", "not-an-overlay"] };
    await installFixtures({ scenario: "shop", readOnly: true, overlays: ["no-daemons"] });
    expect(probe().overlays).toEqual(["no-daemons"]);
    expect((await connectClient(DaemonRegistryService).listDaemons({})).daemons).toEqual([]);
  });

  it("records an rpc that reaches fetch, and throws it", async () => {
    await installFixtures({ scenario: "shop", readOnly: true, overlays: [] });
    await expect(fetch("https://api.example.test/reliant.v1.ChatService/ListChats", { method: "POST" })).rejects.toThrow(/escaped/);
    expect(probe().escaped).toEqual(["POST https://api.example.test/reliant.v1.ChatService/ListChats"]);
  });

  it("refuses a scenario this build does not serve", async () => {
    await expect(installFixtures({ scenario: "shopfront", readOnly: true, overlays: [] })).rejects.toThrow(/no scenario "shopfront"/);
  });
});
