// Copyright (c) 2025 Reliant Labs

/**
 * The public demo's pack on the fixture transport: it survives the wire
 * (checkFixturePack over every handler), it answers no write from fixtures
 * (`answers` is for app-layer and harness packs only), and the one path that
 * mixes it with live traffic (createShopTransport's passthrough, the demo's
 * DEMO_DATA_SOURCE=remote) refuses a pack that does.
 */

import { createRouterTransport } from "@connectrpc/connect";
import { describe, expect, it, vi } from "vitest";

import { createShopTransport, shopPack } from "../shop";
import { assertPureFixturePack, checkFixturePack, defineFixturePack, fixturePackAnswers } from "../transport";
import { allServices } from "./allServices";

vi.mock("../transport", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../transport")>();
  return { ...actual, assertPureFixturePack: vi.fn(actual.assertPureFixturePack) };
});

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

  it("answers no write from fixtures, in any layer it extends", () => {
    expect(fixturePackAnswers(pack)).toEqual([]);
  });

  it("is checked on the passthrough path, which can reach the live gateway", () => {
    createShopTransport({ passthrough: { fallback: createRouterTransport(() => {}) } });
    expect(assertPureFixturePack).toHaveBeenCalledWith(expect.objectContaining({ name: "shop" }), "createShopTransport({ passthrough })");
  });
});
