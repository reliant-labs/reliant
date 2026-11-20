// Copyright (c) 2026 Reliant Labs

/**
 * STUB for the path builder the parallel U6e OVERVIEW branch adds
 * (u6e-briefing.md, "1. NAV+MOBILE"): the Observability Overview page,
 * `consolePath(app, { kind: "obs-overview" })` → `/forge/p/<app>/observability`.
 * The nav matches with this same builder, so swapping the import fixes the link
 * and the lit row at once.
 *
 * TODO(U6e OVERVIEW): import obsOverviewPath from Forge/Console/paths.ts and delete this file.
 */

import type { PathContext } from "@/lib/paths";

import { consoleBase } from "../../../Forge/Console/paths";

export function obsOverviewPath(app: string, ctx?: PathContext): string {
  return `${consoleBase(app, ctx)}/observability`;
}
