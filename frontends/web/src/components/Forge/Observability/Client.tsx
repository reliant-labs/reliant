// Copyright (c) 2025 Reliant Labs

/**
 * Which client a session, trace or error came from, as the console shows it:
 * "Chrome 156 · macOS 15.1" with a device icon, or "Desktop app 1.8.16 ·
 * Electron 41" for a desktop renderer. The full detail is the title, and says
 * which parts the platform derived (browser, OS and device, from the request's
 * own User-Agent) and which the page reported (desktop versions, viewport).
 * Renders nothing when nothing is known.
 */

import { Bot, Monitor, Smartphone, Tablet, type LucideIcon } from "lucide-react";

import { hasClient, type ClientInfo, type DeviceType } from "@/services/controlPlane/observability/client";
import { cn } from "@/lib/utils";

const DEVICE_ICON: Record<Exclude<DeviceType, "">, LucideIcon> = {
  desktop: Monitor,
  mobile: Smartphone,
  tablet: Tablet,
  bot: Bot,
};

/** "156.0.0" → "156"; "15.1.0" → "15.1". Versions are long; the label keeps what tells them apart. */
export function shortVersion(version: string, parts = 1): string {
  return version.split(".").slice(0, parts).join(".");
}

/** The client: "Chrome 156", "Desktop app 1.8.16", "" when unknown. */
export function clientLabel(c: ClientInfo): string {
  if (c.browserName === "Electron" || (c.desktopVersion && !c.browserName)) {
    return c.desktopVersion ? `Desktop app ${c.desktopVersion}` : "Desktop app";
  }
  if (!c.browserName) return "";
  return c.browserVersion ? `${c.browserName} ${shortVersion(c.browserVersion)}` : c.browserName;
}

/** The OS: "macOS 15.1", "" when unknown. */
export function osLabel(c: ClientInfo): string {
  if (!c.osName) return "";
  return c.osVersion ? `${c.osName} ${shortVersion(c.osVersion, 2)}` : c.osName;
}

/** Every known detail, one per line: the title of a client label. */
export function clientTitle(c: ClientInfo): string {
  const lines: string[] = [];
  const browser = [c.browserName, c.browserVersion].filter(Boolean).join(" ");
  const os = [c.osName, c.osVersion].filter(Boolean).join(" ");
  const head = [browser, os && `on ${os}`].filter(Boolean).join(" ");
  if (head || c.deviceType) lines.push([head, c.deviceType].filter(Boolean).join(" · "));
  if (c.chromiumVersion && c.browserName !== "Chrome") lines.push(`Chromium ${c.chromiumVersion}`);
  const electron = c.electronVersion || (c.browserName === "Electron" ? c.browserVersion : "");
  if (c.desktopVersion || electron) {
    lines.push([c.desktopVersion && `Desktop app ${c.desktopVersion}`, electron && `Electron ${electron}`].filter(Boolean).join(" · "));
  }
  if (c.viewportWidth && c.viewportHeight) {
    const screen = c.screenWidth && c.screenHeight ? ` · screen ${c.screenWidth}×${c.screenHeight}` : "";
    lines.push(`Viewport ${c.viewportWidth}×${c.viewportHeight}${screen}`);
  }
  lines.push("Browser, OS and device are derived by the platform from the request's User-Agent; the desktop versions and viewport are what the page reported.");
  return lines.join("\n");
}

/** One line: the device icon, the client (and a desktop app's Electron), and the OS. */
export function ClientSummary({ client, className, testId }: { client: ClientInfo; className?: string; testId?: string }) {
  if (!hasClient(client)) return null;
  const Icon = client.deviceType ? DEVICE_ICON[client.deviceType] : null;
  const label = clientLabel(client);
  const os = osLabel(client);
  const electron = client.browserName === "Electron" ? shortVersion(client.browserVersion || client.electronVersion) : "";
  return (
    <span className={cn("inline-flex min-w-0 items-center gap-1", className)} title={clientTitle(client)} data-testid={testId}>
      {Icon && (
        <>
          <Icon className="h-3 w-3 shrink-0" aria-hidden="true" />
          <span className="sr-only">{client.deviceType}</span>
        </>
      )}
      <span className="truncate">{[label, electron && `Electron ${electron}`, os].filter(Boolean).join(" · ")}</span>
    </span>
  );
}

/** The device: its icon and type, the OS, and the viewport the page reported. For a table's Device column. */
export function DeviceSummary({ client, className, testId }: { client: ClientInfo; className?: string; testId?: string }) {
  if (!client.deviceType && !client.osName) return <span className={cn("text-ink-muted", className)}>—</span>;
  const Icon = client.deviceType ? DEVICE_ICON[client.deviceType] : null;
  const viewport = client.viewportWidth && client.viewportHeight ? `${client.viewportWidth}×${client.viewportHeight}` : "";
  return (
    <span className={cn("flex min-w-0 flex-col leading-tight", className)} title={clientTitle(client)} data-testid={testId}>
      <span className="inline-flex min-w-0 items-center gap-1 text-ink">
        {Icon && <Icon className="h-3 w-3 shrink-0" aria-hidden="true" />}
        <span className="truncate">{osLabel(client) || client.deviceType}</span>
      </span>
      {(client.deviceType || viewport) && (
        <span className="truncate text-2xs text-ink-muted">{[client.deviceType, viewport].filter(Boolean).join(" · ")}</span>
      )}
    </span>
  );
}

/** The client alone: "Chrome 156", or "Desktop app 1.8.16" over "Electron 41.10.7". For a table's Client column. */
export function ClientName({ client, className, testId }: { client: ClientInfo; className?: string; testId?: string }) {
  const label = clientLabel(client);
  if (!label) return <span className={cn("text-ink-muted", className)}>—</span>;
  const electron = client.browserName === "Electron" ? client.browserVersion || client.electronVersion : "";
  return (
    <span className={cn("flex min-w-0 flex-col leading-tight", className)} title={clientTitle(client)} data-testid={testId}>
      <span className="truncate text-ink">{label}</span>
      {electron && <span className="truncate text-2xs text-ink-muted">Electron {electron}</span>}
    </span>
  );
}
