// Copyright (c) 2025 Reliant Labs

/**
 * Which client and which builds one trace ran on, from its spans: the browser
 * spans' resource names the client (the platform-derived user_agent.*, os.*,
 * reliant.device.type and the page's app.desktop.*) and the frontend build;
 * every other span names a backend build. The same split ListTraces makes
 * server-side (TraceSummary.client/frontendVersion/backendBuilds), for a trace
 * opened on its own.
 */

import { NO_CLIENT, type ClientInfo, type DeviceType, type ServiceBuild } from "@/services/controlPlane/observability/client";
import type { Span } from "@/services/controlPlane/observability/types";

import { isBrowserSpan } from "./traceTitle";

export interface TraceBuilds {
  client: ClientInfo;
  frontendVersion: string;
  backendBuilds: ServiceBuild[];
}

const DEVICE_TYPES = new Set<string>(["desktop", "mobile", "tablet", "bot"]);

export function traceBuildsOf(spans: readonly Pick<Span, "resource" | "service">[]): TraceBuilds {
  let client: ClientInfo = NO_CLIENT;
  let frontendVersion = "";
  const backends = new Map<string, ServiceBuild>();
  for (const span of spans) {
    const r = span.resource;
    const version = r["service.version"] ?? "";
    if (isBrowserSpan(span)) {
      if (!frontendVersion && version) frontendVersion = version;
      if (client === NO_CLIENT && (r["user_agent.name"] || r["app.desktop.version"])) {
        const device = r["reliant.device.type"] ?? "";
        client = {
          ...NO_CLIENT,
          browserName: r["user_agent.name"] ?? "",
          browserVersion: r["user_agent.version"] ?? "",
          osName: r["os.name"] ?? "",
          osVersion: r["os.version"] ?? "",
          deviceType: (DEVICE_TYPES.has(device) ? device : "") as DeviceType,
          chromiumVersion: r["reliant.chromium.version"] ?? "",
          desktopVersion: r["app.desktop.version"] ?? "",
          electronVersion: r["user_agent.name"] === "Electron" ? (r["user_agent.version"] ?? "") : (r["app.desktop.electron.version"] ?? ""),
        };
      }
    } else if (version) {
      const service = span.service || r["service.name"] || "";
      backends.set(`${service}\u0000${version}`, { service, version });
    }
  }
  const backendBuilds = [...backends.values()].sort((a, b) => a.service.localeCompare(b.service) || a.version.localeCompare(b.version));
  return { client, frontendVersion, backendBuilds };
}
