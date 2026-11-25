// Copyright (c) 2026 Reliant Labs

/**
 * The app's shell around the demo's pages (demoShellRoutes.tsx): the app's
 * keyboard host, with none of the app's overlays (no palette, no chat
 * search, no project switcher: they are the app's), around the app's frame
 * (Layout/shell/AppShell.tsx) with the demo's nav (DemoNav) and no Setup
 * Guide (the demo has nothing to set up).
 *
 * Its own chunk, loaded beside the first page's (every page of the demo is a
 * lazy chunk already, so the frame is on no longer a path than the page it
 * holds), which keeps the frame, the keyboard host and the nav out of the
 * entry chunk every visitor downloads first (scripts/demo/bundle-budget.json).
 */

import type { ReactNode } from "react";

import { AppShellFrame } from "@/components/Layout/shell/AppShell";
import { ShortcutHost } from "@/components/Shortcuts/ShortcutHost";

import { DemoNav } from "./DemoNav";

export function DemoShellFrame({ children }: { children: ReactNode }) {
  return (
    <ShortcutHost>
      <AppShellFrame nav={DemoNav} publicPreview>{children}</AppShellFrame>
    </ShortcutHost>
  );
}

export default DemoShellFrame;
