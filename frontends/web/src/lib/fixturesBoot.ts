// Copyright (c) 2025 Reliant Labs

/**
 * Fixture mode's boot step (lib/fixturesConfig.ts says what turns it on).
 * main.tsx awaits bootFixtures() before it renders anything, so the first
 * RPC, the first session read and the first fetch already meet the fixture
 * transport and src/fixtures/outside.ts.
 *
 * The fixture code is a LAZY chunk: this module is all fixture mode adds to
 * the app's entry chunk, and a document without FIXTURES_SCENARIO never
 * loads the chunk (vite-plugin-fixture-entry-guard.ts fails the build if any
 * src/fixtures module reaches the entry chunk's static graph).
 *
 * It fails closed: when the pack cannot install, the page shows why and the
 * boot stops, rather than rendering the app against the live backends a
 * fixture document was never meant to reach.
 */

import { runtimeFixtures } from "./fixturesConfig";

let booting: Promise<void> | null = null;

/** Install fixture mode when the runtime config asks for it; resolves at once when it does not. */
export function bootFixtures(): Promise<void> {
  if (booting) return booting;
  const config = runtimeFixtures();
  booting = config ? import("@/fixtures/install").then(({ installFixtures }) => installFixtures(config)) : Promise.resolve();
  return booting;
}
