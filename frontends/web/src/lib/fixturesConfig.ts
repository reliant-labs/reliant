// Copyright (c) 2025 Reliant Labs

/**
 * FIXTURE MODE's switch, and the ONE reader of the runtime-config keys that
 * set it. A deployment turns it on in its config.js, never in the build, so
 * the bytes that serve app.reliantlabs.io are the bytes the visual harness
 * screenshots and the hosted preview serves:
 *
 *   FIXTURES_SCENARIO   the fixture pack to serve ("shop"). Present and
 *                       non-empty: every RPC is answered in the page, by the
 *                       pack, behind the app's real interceptors
 *                       (src/fixtures/install.ts). Absent: the live app, and
 *                       not one byte of fixture code loads.
 *   FIXTURES_READ_ONLY  anything but exactly "false" refuses every write; a
 *                       missing key fails closed.
 *
 * The keys are forge's (forge#2719: `fixtures = forge.Fixtures { scenario =
 * "shop" }` on a frontend renders both), read raw from window.__FORGE_CONFIG__
 * because config_gen.ts's schema is this app's own keys and drops others. Only
 * deploy/kcl/preview may set them (internal/forgetest/fixtures_env_test.go).
 *
 * In fixture mode, and only then, the URL's `?fixtures=<name>[,<name>]` names
 * overlays: variants of the pack (no machines, a service missing, …) that
 * install.ts allow-lists by name. A live page never reads the parameter.
 *
 * Import-free on purpose: lib/fixturesBoot.ts reads this in the app's entry
 * chunk, which carries no fixture code (vite-plugin-fixture-entry-guard.ts).
 */

/** What a document that turns fixture mode on asks for. */
export interface RuntimeFixtures {
  /** The fixture pack to serve, e.g. "shop". */
  readonly scenario: string;
  /** False only when FIXTURES_READ_ONLY is exactly "false". */
  readonly readOnly: boolean;
  /** Overlay names from `?fixtures=`, in order, unvalidated (install.ts allow-lists them). */
  readonly overlays: readonly string[];
}

/** The URL parameter naming overlays, read only in fixture mode. */
export const FIXTURES_URL_PARAM = "fixtures";

const OVERLAY_NAME = /^[a-z0-9][a-z0-9:/._-]{0,127}$/i;

/**
 * Fixture mode's settings, or null when this document does not turn it on.
 * Both arguments default to the page's own (config.js and the address bar).
 */
export function runtimeFixtures(
  doc: Record<string, unknown> | undefined = typeof window !== "undefined" ? window.__FORGE_CONFIG__ : undefined,
  search: string = typeof window !== "undefined" ? window.location.search : "",
): RuntimeFixtures | null {
  const scenario = typeof doc?.FIXTURES_SCENARIO === "string" ? doc.FIXTURES_SCENARIO.trim() : "";
  if (!scenario) return null;
  const overlays = (new URLSearchParams(search).get(FIXTURES_URL_PARAM) ?? "")
    .split(",")
    .map((name) => name.trim())
    .filter((name) => OVERLAY_NAME.test(name));
  return { scenario, readOnly: doc?.FIXTURES_READ_ONLY !== "false", overlays };
}

/** True when this page serves a fixture pack instead of the live backends. */
export function isFixtureMode(): boolean {
  return runtimeFixtures() !== null;
}
