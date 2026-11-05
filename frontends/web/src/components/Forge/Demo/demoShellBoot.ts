// Copyright (c) 2025 Reliant Labs

/**
 * The demo shell's boot (demoShellRoutes.tsx), once, before anything renders
 * the tour:
 *
 *  - the tour runs as the forge shell does (forge copy, `forge …` commands);
 *  - its progress lives in memory only, so a finish is never remembered and
 *    the next visit starts the tour again (DemoAutoStart);
 *  - telemetry starts for an anonymous visitor (lib/demoTelemetry.ts decides
 *    what it records, and nothing for a browser that opted out).
 */

import { memoryTourStorage, setTourShell, setTourStorage } from "@/components/Onboarding/tourShell";
import { initTelemetry } from "@/lib/telemetry";

export function bootDemoShell(): void {
  setTourShell("forge");
  setTourStorage(memoryTourStorage());
  void initTelemetry();
}
