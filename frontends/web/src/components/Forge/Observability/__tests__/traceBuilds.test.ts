// @vitest-environment node
import { describe, expect, it } from "vitest";

import { NO_CLIENT } from "@/services/controlPlane/observability/client";

import { traceBuildsOf } from "../traceBuilds";

const browser = (resource: Record<string, string>) => ({ service: "reliant-web", resource: { "rum.sessionId": "s1", "reliant.ingest.class": "browser", "service.name": "reliant-web", ...resource } });
const server = (service: string, version: string) => ({ service, resource: { "service.name": service, "service.version": version } });

describe("traceBuildsOf", () => {
  it("names the browser's client and frontend build and every backend build", () => {
    const builds = traceBuildsOf([
      browser({
        "service.version": "1.8.16-dev+7d787d80",
        "user_agent.name": "Electron",
        "user_agent.version": "41.10.7",
        "os.name": "macOS",
        "os.version": "15.1.0",
        "reliant.device.type": "desktop",
        "app.desktop.version": "1.8.16",
      }),
      server("reliant-api-server", "v1.7.92"),
      server("reliant-temporal-worker", "v1.7.92"),
      server("reliant-api-server", "v1.7.92"),
    ]);
    expect(builds.frontendVersion).toBe("1.8.16-dev+7d787d80");
    expect(builds.frontendService).toBe("reliant-web");
    expect(builds.client).toMatchObject({ browserName: "Electron", electronVersion: "41.10.7", osName: "macOS", deviceType: "desktop", desktopVersion: "1.8.16" });
    expect(builds.backendBuilds).toEqual([
      { service: "reliant-api-server", version: "v1.7.92" },
      { service: "reliant-temporal-worker", version: "v1.7.92" },
    ]);
  });

  it("has no client or frontend for a trace no browser started, and ignores an unknown device type", () => {
    expect(traceBuildsOf([server("api", "v1")])).toEqual({ client: NO_CLIENT, frontendService: "", frontendVersion: "", backendBuilds: [{ service: "api", version: "v1" }] });
    expect(traceBuildsOf([browser({ "user_agent.name": "Chrome", "reliant.device.type": "toaster" })]).client.deviceType).toBe("");
  });
});
