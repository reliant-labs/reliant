// Copyright (c) 2025 Reliant Labs

/**
 * The public demo's pack on the fixture transport: it survives the wire
 * (checkFixturePack over every handler), it answers from fixtures no rpc but
 * DEMO_PACK_ANSWERS (held by shop/__tests__/demoPackAnswers.test.ts), and the
 * one path that mixes it with live traffic (createShopTransport's
 * passthrough, the demo's DEMO_DATA_SOURCE=remote) honours none of them:
 * forge's mock transport, `readOnly: true`, NO_SIDE_EFFECTS only.
 */

import { createClient, createRouterTransport } from "@connectrpc/connect";
import { describe, expect, it, vi } from "vitest";

import { UserService } from "@/gen/controlplane/services/user/v1/user_pb";

import { createShopTransport, DEMO_PACK_ANSWERS, SHOP_READ_ONLY_MESSAGE, shopPack } from "../shop";
import { checkFixturePack, defineFixturePack, fixturePackAnswers } from "../transport";
import { allServices } from "./allServices";

const FIXED_NOW = Date.parse("2026-11-18T15:30:00Z");

describe("the shop pack (the public demo's)", () => {
  // Pinned before anything reads the pack (shopStory.ts pinClock).
  const pack = shopPack({ now: FIXED_NOW });

  it("answers every read with a message that survives the wire", async () => {
    // Every generated service, not a hand-kept list: the pack gains services as handlers land.
    expect(await checkFixturePack(pack, { services: allServices(), now: () => FIXED_NOW })).toEqual([]);
  });

  it("(the check still names a key whose service is not generated at all)", async () => {
    const stray = defineFixturePack({ name: "stray", scenario: { "acme.v1.NoSuchService/GetThing": () => ({}) } });
    expect(await checkFixturePack(stray, { services: allServices() })).toEqual([
      { pack: "stray", method: "acme.v1.NoSuchService/GetThing", message: "cannot check: acme.v1.NoSuchService is not in options.services" },
    ]);
  });

  it("answers from fixtures no rpc but DEMO_PACK_ANSWERS, in any layer it extends", () => {
    const listed = DEMO_PACK_ANSWERS.map((a) => `${a.method.parent.typeName}/${a.method.name}`);
    for (const key of fixturePackAnswers(pack)) expect(listed, key).toContain(key);
  });

  it("refuses an undeclared read on the passthrough path before the live gateway sees it", async () => {
    const gateway = vi.fn();
    const fallback = createRouterTransport((router) => router.service(UserService, { getCurrentUser: gateway }));
    const transport = createShopTransport({ passthrough: { fallback } });
    // GetCurrentUser writes on read, so it declares no NO_SIDE_EFFECTS.
    await expect(createClient(UserService, transport).getCurrentUser({})).rejects.toThrow(SHOP_READ_ONLY_MESSAGE);
    expect(gateway).not.toHaveBeenCalled();
  });
});
