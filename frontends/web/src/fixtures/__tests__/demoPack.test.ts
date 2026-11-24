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

import { TestsService } from "@/gen/controlplane/services/tests/v1/tests_pb";
import { VulnerabilityService } from "@/gen/controlplane/services/vulnerability/v1/vulnerability_pb";
import { CoverageSourceService } from "@/gen/reliant/v1/coverage_source_pb";

import { createShopTransport, shopPack } from "../shop";
import { DEMO_DEFAULT_SERVICES } from "../shop/handlers/defaults";
import { assertPureFixturePack, checkFixturePack, fixturePackAnswers } from "../transport";

vi.mock("../transport", async (importOriginal) => {
  const actual = await importOriginal<typeof import("../transport")>();
  return { ...actual, assertPureFixturePack: vi.fn(actual.assertPureFixturePack) };
});

const FIXED_NOW = Date.parse("2026-11-18T15:30:00Z");

describe("the shop pack (the public demo's)", () => {
  // Pinned before anything reads the pack (shopStory.ts pinClock).
  const pack = shopPack({ now: FIXED_NOW });

  it("answers every read with a message that survives the wire", async () => {
    // Story-only services have explicit handlers, not generated demo defaults.
    const services = [...DEMO_DEFAULT_SERVICES, VulnerabilityService, TestsService, CoverageSourceService];
    expect(await checkFixturePack(pack, { services, now: () => FIXED_NOW })).toEqual([]);
  });

  it("answers no write from fixtures, in any layer it extends", () => {
    expect(fixturePackAnswers(pack)).toEqual([]);
  });

  it("is checked on the passthrough path, which can reach the live gateway", () => {
    createShopTransport({ passthrough: { fallback: createRouterTransport(() => {}) } });
    expect(assertPureFixturePack).toHaveBeenCalledWith(expect.objectContaining({ name: "shop" }), "createShopTransport({ passthrough })");
  });
});
