// Copyright (c) 2025 Reliant Labs

/**
 * Which client a browser session, trace or error came from
 * (controlplane.v1.ClientContext), shared by sessions, traces and issues.
 *
 * Two sources, never mixed (control-plane docs/observability/client-context.md):
 * the browser, OS, device type and Chromium version are the PLATFORM's, parsed
 * at ingest from the request's own User-Agent and Sec-CH-UA headers; the
 * desktop versions and the viewport are what the PAGE reported. An empty
 * string or a 0 is unknown.
 */

import type { ClientContext as WireClientContext, ServiceBuild as WireServiceBuild } from "@/gen/controlplane/controlplane/v1/client_pb";

export type DeviceType = "desktop" | "mobile" | "tablet" | "bot" | "";

export interface ClientInfo {
  /** "Chrome", "Mobile Safari", "Firefox", "Edge", "Electron" (a desktop app's renderer), ... */
  browserName: string;
  browserVersion: string;
  /** "macOS", "Windows", "iOS", "Android", "Linux", "ChromeOS", ... */
  osName: string;
  osVersion: string;
  deviceType: DeviceType;
  chromiumVersion: string;
  /** The desktop app's own version, and the Electron it runs on. Empty outside the desktop app. */
  desktopVersion: string;
  electronVersion: string;
  /** CSS pixels; sessions only. 0: unknown. */
  viewportWidth: number;
  viewportHeight: number;
  screenWidth: number;
  screenHeight: number;
}

export const NO_CLIENT: ClientInfo = Object.freeze({
  browserName: "",
  browserVersion: "",
  osName: "",
  osVersion: "",
  deviceType: "",
  chromiumVersion: "",
  desktopVersion: "",
  electronVersion: "",
  viewportWidth: 0,
  viewportHeight: 0,
  screenWidth: 0,
  screenHeight: 0,
});

const DEVICE_TYPES: readonly DeviceType[] = ["desktop", "mobile", "tablet", "bot"];

export function mapClient(c: WireClientContext | undefined): ClientInfo {
  if (!c) return NO_CLIENT;
  return {
    browserName: c.browserName,
    browserVersion: c.browserVersion,
    osName: c.osName,
    osVersion: c.osVersion,
    deviceType: DEVICE_TYPES.includes(c.deviceType as DeviceType) ? (c.deviceType as DeviceType) : "",
    chromiumVersion: c.chromiumVersion,
    desktopVersion: c.desktopVersion,
    electronVersion: c.electronVersion,
    viewportWidth: c.viewportWidth,
    viewportHeight: c.viewportHeight,
    screenWidth: c.screenWidth,
    screenHeight: c.screenHeight,
  };
}

/** A backend build that served a trace: a server span's service and version. */
export interface ServiceBuild {
  service: string;
  version: string;
}

export function mapServiceBuilds(builds: readonly WireServiceBuild[] | undefined): ServiceBuild[] {
  return (builds ?? []).map((b) => ({ service: b.service, version: b.version }));
}

/** Whether anything about the client is known. */
export function hasClient(c: ClientInfo): boolean {
  return !!(c.browserName || c.osName || c.deviceType || c.desktopVersion);
}
