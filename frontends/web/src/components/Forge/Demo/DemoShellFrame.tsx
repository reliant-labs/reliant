// Copyright (c) 2026 Reliant Labs

/**
 * The app's shell around the demo's pages (demoShellRoutes.tsx): the app's
 * keyboard host with the app's overlays (the command palette Find opens, chat
 * search and the project switcher), running for the demo's fixed visitor
 * (demoShellRoutes.tsx DEMO_SESSION), around the app's frame
 * (Layout/shell/AppShell.tsx) with the demo's nav (DemoNav) and no Setup
 * Guide (the demo has nothing to set up). The palette is the app's without
 * the account's pages (DemoCommandPalette); each overlay loads on first open,
 * as the app's do (routes.tsx APP_OVERLAYS).
 *
 * Its own chunk, loaded beside the first page's (every page of the demo is a
 * lazy chunk already, so the frame is on no longer a path than the page it
 * holds), which keeps the frame, the keyboard host and the nav out of the
 * entry chunk every visitor downloads first (scripts/demo/bundle-budget.json).
 */

import type { ReactNode } from "react";

import { AppShellFrame } from "@/components/Layout/shell/AppShell";
import { ShortcutHost, type ShortcutOverlays } from "@/components/Shortcuts/ShortcutHost";
import { lazyRoute } from "@/lib/lazyRoute";

import { DemoNav } from "./DemoNav";

const DEMO_OVERLAYS: ShortcutOverlays = {
  projectSwitcher: lazyRoute(() => import("@/components/Projects/SwitchProjectDialog"), "SwitchProjectDialog"),
  commandPalette: lazyRoute(() => import("./DemoCommandPalette"), "DemoCommandPalette"),
  chatSearch: lazyRoute(() => import("@/components/Layout/ChatSearch"), "ChatSearch"),
};

export function DemoShellFrame({ children }: { children: ReactNode }) {
  return (
    <ShortcutHost overlays={DEMO_OVERLAYS}>
      <AppShellFrame nav={DemoNav} publicPreview>{children}</AppShellFrame>
    </ShortcutHost>
  );
}

export default DemoShellFrame;
