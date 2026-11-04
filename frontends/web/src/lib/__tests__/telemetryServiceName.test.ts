/**
 * The service.name the web app's telemetry reports must be the one forge's
 * generated observability filters on. They were derived apart once — the
 * runtime said "reliant-web" while the alert defaults filtered on
 * "reliant-frontend" — and every web alert matched nothing.
 *
 * Both now come from forge's one derivation ("<project>-<frontend>"): the
 * runtime passes otel_gen.ts's SERVICE_NAME, and the dashboards and alert
 * defaults under deploy/observability/ are generated from the same name. This
 * test reads the generated files themselves, so a regenerate that moves either
 * side fails here.
 */
import fs from "node:fs";
import path from "node:path";
import { describe, expect, it } from "vitest";

import { SERVICE_NAME } from "../otel_gen";
import { buildTelemetryConfig } from "../telemetry";

const OBSERVABILITY = path.resolve(__dirname, "../../../../../deploy/observability");

function readJSON<T>(rel: string): T {
  return JSON.parse(fs.readFileSync(path.join(OBSERVABILITY, rel), "utf-8")) as T;
}

interface AlertDefaults {
  input: { frontend_services?: string[] };
  defaults: Array<{ id: string; signal: string }>;
}

interface HyperDXDashboard {
  tiles: Array<{ id: string; config: { where?: string } }>;
}

describe("telemetry service.name", () => {
  const reported = buildTelemetryConfig({
    config: {},
    prerelease: false,
    buildVersion: "0.0.0",
    replayRate: 0,
  }).serviceName;

  it("is the frontend's forge-derived SERVICE_NAME", () => {
    expect(SERVICE_NAME).toBe("reliant-web");
    expect(reported).toBe(SERVICE_NAME);
  });

  it("is the service the generated frontend alert defaults filter on", () => {
    const doc = readJSON<AlertDefaults>("alert-defaults_gen.json");
    expect(doc.input.frontend_services).toEqual([reported]);
  });

  it("scopes every browser panel of the generated frontend dashboard", () => {
    const dash = readJSON<HyperDXDashboard>("dashboards/reliant-frontend_gen.json");
    const browser = dash.tiles.filter((t) => t.config.where?.includes("rum.sessionId"));
    expect(browser.length).toBeGreaterThan(0);
    for (const tile of browser) {
      expect(tile.config.where, tile.id).toContain(`ServiceName IN ('${reported}')`);
    }
  });
});
