// Copyright (c) 2025 Reliant Labs

/** The browsers shop's customers use, as the platform derives them (traces, sessions, issues). */

import { NO_CLIENT, type ClientInfo } from "@/services/controlPlane/observability/client";

export const DEMO_CLIENTS: readonly ClientInfo[] = [
  { ...NO_CLIENT, browserName: "Chrome", browserVersion: "142.0.0", osName: "macOS", osVersion: "15.1.0", deviceType: "desktop", chromiumVersion: "142.0.7444.59", viewportWidth: 1440, viewportHeight: 900, screenWidth: 2560, screenHeight: 1440 },
  { ...NO_CLIENT, browserName: "Mobile Safari", browserVersion: "18.6", osName: "iOS", osVersion: "18.6", deviceType: "mobile", viewportWidth: 390, viewportHeight: 844, screenWidth: 390, screenHeight: 844 },
  { ...NO_CLIENT, browserName: "Chrome", browserVersion: "142.0.0", osName: "Android", osVersion: "15", deviceType: "mobile", chromiumVersion: "142.0.7444.59", viewportWidth: 412, viewportHeight: 915, screenWidth: 412, screenHeight: 915 },
  { ...NO_CLIENT, browserName: "Firefox", browserVersion: "144.0", osName: "Windows", osVersion: "11", deviceType: "desktop", viewportWidth: 1920, viewportHeight: 969, screenWidth: 1920, screenHeight: 1080 },
  { ...NO_CLIENT, browserName: "Safari", browserVersion: "18.6", osName: "macOS", osVersion: "15.6", deviceType: "desktop", viewportWidth: 1512, viewportHeight: 857, screenWidth: 1512, screenHeight: 982 },
];
