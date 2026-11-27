// Copyright (c) 2026 Reliant Labs

/**
 * The public demo's command palette (Find in the nav, F, the palette's
 * chord): the app's own (Layout/CommandPalette.tsx) without the commands
 * that open the signed-in account's pages, which the demo has none of
 * (the nav leaves the same pages out, demoNavConfig.ts DEMO_EXCLUDED_IDS).
 * DemoShellFrame loads it on first open, as the app loads its palette.
 */

import type { ComponentProps } from "react";

import { CommandPalette } from "@/components/Layout/CommandPalette";

/** The palette's commands for Usage & billing and Settings and its sections. */
export const DEMO_PALETTE_OMIT: readonly string[] = [
  "usage-billing",
  "settings",
  "shortcuts",
  "appearance",
  "providers",
  "privacy",
  "developer",
  "about",
];

export function DemoCommandPalette(props: Omit<ComponentProps<typeof CommandPalette>, "omitCommands">) {
  return <CommandPalette {...props} omitCommands={DEMO_PALETTE_OMIT} />;
}
