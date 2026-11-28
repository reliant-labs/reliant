/**
 * The app pack's named overlays (shop/overlays.ts), each served the way
 * fixture mode serves it: over the app pack, through a read-only fixture
 * transport, with Connect's real serialization.
 */
import { createClient } from "@connectrpc/connect";
import { afterEach, beforeEach, describe, expect, it } from "vitest";

import { TestsService } from "@/gen/controlplane/services/tests/v1/tests_pb";
import { BackgroundService } from "@/gen/reliant/v1/background_pb";
import { ChatService } from "@/gen/reliant/v1/chat_pb";
import { DaemonRegistryService } from "@/gen/reliant/v1/daemon_registry_pb";
import { ProjectService } from "@/gen/reliant/v1/project_pb";
import { SettingsService } from "@/gen/reliant/v1/settings_pb";
import { StreamingService } from "@/gen/reliant/v1/streaming_pb";
import { TokenService } from "@/gen/reliant/v1/token_pb";
import { UsageService } from "@/gen/reliant/v1/usage_pb";

import { createFixtureTransport, defineFixturePack } from "../../transport";
import { shopAppPack } from "../app";
import { SHOP_READ_ONLY_MESSAGE } from "../index";
import { PENDING_LIST_CHATS, SHOP_OVERLAYS } from "../overlays";
import { demoNow, resetDemoNowForTests } from "../shopStory";
import { CHAT_IDS, PROJECT_IDS } from "../workspace";

const FIXED_NOW = Date.UTC(2026, 10, 18, 15, 30);

/** A transport serving the app pack with `overlays` on top, as install.ts composes them. */
function served(...overlays: string[]) {
  const app = shopAppPack({ now: FIXED_NOW });
  const pack = defineFixturePack({ name: "test", extends: [app, ...overlays.map((name) => SHOP_OVERLAYS[name]())] });
  return createFixtureTransport({ pack, now: demoNow, readOnly: true, readOnlyMessage: SHOP_READ_ONLY_MESSAGE });
}

beforeEach(() => resetDemoNowForTests());
afterEach(() => {
  delete window.__forgeFixtures;
});

describe("the shop app pack's overlays", () => {
  it("no-daemons: Alex has no machine", async () => {
    expect((await createClient(DaemonRegistryService, served()).listDaemons({})).daemons.length).toBeGreaterThan(0);
    expect((await createClient(DaemonRegistryService, served("no-daemons")).listDaemons({})).daemons).toEqual([]);
  });

  it("tests-unimplemented: every TestsService read answers Unimplemented", async () => {
    const tests = createClient(TestsService, served("tests-unimplemented"));
    await expect(tests.getTestSummary({ project: "shop" })).rejects.toThrow(/TestsService is not served here/);
  });

  it("chat-no-failed-send: the stream's snapshot leaves out the client-only failed send", async () => {
    const first = async (transport: ReturnType<typeof served>) => {
      const abort = new AbortController();
      for await (const event of createClient(StreamingService, transport).streamChatUpdates({ chatId: CHAT_IDS.cartTotals }, { signal: abort.signal })) {
        abort.abort();
        return event.event.case === "snapshot" ? event.event.value.messages.map((m) => m.id) : [];
      }
      return [];
    };
    const all = await first(served());
    const without = await first(served("chat-no-failed-send"));
    expect(all.some((id) => id.startsWith("optimistic-"))).toBe(true);
    expect(without).toEqual(all.filter((id) => !id.startsWith("optimistic-")));
  });

  it("project-selector: Alex's settings plus two pins", async () => {
    const base = await createClient(SettingsService, served()).listSettings({});
    const pinned = await createClient(SettingsService, served("project-selector")).listSettings({});
    expect(pinned.total).toBe(base.total + 1);
    const pins = pinned.settings.find((s) => s.key === "sidebar.projects");
    expect(JSON.parse(pins!.value)).toEqual({ "org-acme": { pinned: [PROJECT_IDS.shop, "app:status-page"] } });
  });

  it("projects-page: the pins, and ledger-worker without code here", async () => {
    const transport = served("projects-page");
    const catalog = await createClient(ProjectService, transport).listProjectsWithApps({});
    expect(catalog.unlinkedApps.map((a) => a.name)).toEqual(["status-page", "ledger-worker"]);
    expect((await createClient(SettingsService, transport).listSettings({})).settings.some((s) => s.key === "sidebar.projects")).toBe(true);
  });

  it("app-code-card: shop deployed from two repositories, held by three projects", async () => {
    const catalog = await createClient(ProjectService, served("app-code-card")).listProjectsWithApps({});
    const shops = catalog.projects.flatMap((row) => row.apps.filter((a) => a.name === "shop").map((a) => a.hosted!));
    expect(shops).toHaveLength(3);
    for (const hosted of shops) {
      expect(hosted.sources.map((s) => s.remote)).toEqual(["github.com/acme/shop", "github.com/alex/shop"]);
      expect(hosted.warning).toMatch(/two repositories/);
    }
  });

  it("empty-reads: an unanswered read is empty (a stream: one empty frame, held open) and noted; a write is still refused", async () => {
    window.__forgeFixtures = { unknown: [] } as never;
    const transport = served("empty-reads");
    expect(await createClient(UsageService, transport).getLLMUsage({})).toBeDefined();
    expect((await createClient(TokenService, transport).listTokens({})).tokens ?? []).toEqual([]);
    const abort = new AbortController();
    for await (const event of createClient(BackgroundService, transport).streamProcessOutput({}, { signal: abort.signal })) {
      expect(event.event.case).toBeUndefined();
      abort.abort();
      break;
    }
    expect((window.__forgeFixtures as unknown as { unknown: string[] }).unknown).toEqual([
      "reliant.v1.UsageService/GetLLMUsage",
      "reliant.v1.TokenService/ListTokens",
      "reliant.v1.BackgroundService/StreamProcessOutput",
    ]);
    // What the pack answers is untouched, and a write is refused, never answered empty.
    expect((await createClient(DaemonRegistryService, transport).listDaemons({})).daemons.length).toBeGreaterThan(0);
    await expect(createClient(ProjectService, transport).touchProject({ projectId: PROJECT_IDS.shop })).rejects.toThrow(SHOP_READ_ONLY_MESSAGE);
  });

  it(`${PENDING_LIST_CHATS}: the chat list never answers until the call is dropped`, async () => {
    const abort = new AbortController();
    const call = createClient(ChatService, served(PENDING_LIST_CHATS)).listChats({}, { signal: abort.signal });
    const outcome = await Promise.race([call.then(() => "answered", () => "failed"), new Promise((r) => setTimeout(() => r("pending"), 50))]);
    expect(outcome).toBe("pending");
    abort.abort();
    await expect(call).rejects.toThrow();
  });
});
