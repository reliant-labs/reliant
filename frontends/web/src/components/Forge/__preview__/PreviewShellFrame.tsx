// Copyright (c) 2026 Reliant Labs

/**
 * DEV-ONLY: the app's shell around a preview harness, as the public demo wears
 * it (Forge/Demo/DemoShellFrame): the app's frame (Layout/shell/AppShell.tsx
 * AppShellFrame) with the shell's own nav column
 * (Layout/sidebar/ShellNavColumn) drawing the console's rows from a nav
 * config (PREVIEW_NAV). The harness is a signed-in reader, so the frame is on
 * by a fixed session (lib/session fixedSession), not the auth store. A
 * screenshot of a harness shows the
 * frame, nav and bar the product shows, not a look-alike.
 *
 * Two ways in:
 *
 *  - PreviewShellFrame: the layout of a router whose console routes sit under
 *    `_authenticated` (ConsoleFixturePreview's memory router). The frame is on
 *    by the app's own rule, the nav reads the router's console path, and the
 *    console draws its own bar (ConsoleLayout, ConsoleFrame).
 *  - ConsolePreviewFrame: one screen at a dev route of its own, outside the
 *    app's layouts (the data and storage browsers, secrets, the trace
 *    latency chart, the chart galleries). The frame is drawn regardless of
 *    route (`always`), the nav lights `page`'s row, and the console's bar
 *    (ConsoleFrame: breadcrumb, environment scope) sits over the screen. It
 *    serves the nav's reads from the shop fixture pack (<FixturePreview>, or
 *    the harness's own pack and overlay), so nothing reaches a backend.
 *
 * The keyboard host is the app's, at the root (routes.tsx RootShell): a
 * second one would see every keystroke twice.
 */

import { createContext, useContext, useMemo, useState, type ReactNode } from "react";

import { ForgeBrand } from "@/components/ForgeShell/ForgeBrand";
import { AppShellFrame, type ShellNavProps } from "@/components/Layout/shell/AppShell";
import {
  analyticsGroup,
  auth,
  configureGroup,
  dashboards,
  data,
  deploymentsGroup,
  observabilityArea,
  overview,
  securityGroup,
  storage,
} from "@/components/Layout/sidebar/navConfig/items";
import { testsGroup } from "@/components/Layout/sidebar/navConfig/tests";
import type { NavConfig } from "@/components/Layout/sidebar/navConfig/types";
import { SidebarOrgRow, SidebarProjectLabel } from "@/components/Layout/sidebar/primitives";
import { ShellNavColumn } from "@/components/Layout/sidebar/ShellNavColumn";
import { useShellProject, type ShellProject } from "@/components/Layout/sidebar/shellProject";
import { useNarrowViewport } from "@/hooks/useNarrowViewport";
import { fixedSession, SessionProvider } from "@/lib/session";
import { FixturePreview, type FixtureServeOptions } from "@/fixtures/preview";
import { SHOP_SERVE, shopPack } from "@/fixtures/shop";
import { DEMO_ENVS, DEMO_PROJECT } from "@/fixtures/shop/handlers/scope";
import type { FixturePack } from "@/fixtures/transport";

import { ConsoleFrame } from "../Console/ConsoleFrame";
import { consolePageOf, consolePath, type ConsolePage } from "../Console/paths";

/** The harness's signed-in reader: a session that never changes, with no token. */
const PREVIEW_SESSION = fixedSession({ id: "preview", email: "preview@example.com", name: "preview" });

/**
 * The console's rows of the app's own nav (navConfig/hybrid.ts), drawn from
 * the same item objects and in its order, as the demo's nav is
 * (Forge/Demo/demoNavConfig.ts): without the app's chats, Inbox, Settings and
 * billing, which a preview of the console has nothing behind.
 */
export const PREVIEW_NAV: NavConfig = {
  items: [overview, dashboards, observabilityArea, analyticsGroup, deploymentsGroup, testsGroup, data, storage, auth, securityGroup, configureGroup],
  footer: [],
};

/** The page a one-screen harness shows (ConsolePreviewFrame), for the nav's current row. */
const PreviewPageContext = createContext<ConsolePage | null>(null);

function PreviewNav({ pathname, search, onNavigate }: ShellNavProps) {
  const page = useContext(PreviewPageContext);
  // A console path is the nav's own; a harness at a route of its own reads as the console page it shows.
  const path = consolePageOf(pathname) ? pathname : consolePath(DEMO_PROJECT, page ?? { kind: "overview" });
  const resolved = useShellProject(path, search);
  const shell = useMemo<ShellProject>(() => ({ ...resolved, mode: "one", ids: resolved.projectId ? [resolved.projectId] : [], app: DEMO_PROJECT, deployed: true }), [resolved]);
  const narrow = useNarrowViewport();
  return (
    <ShellNavColumn
      config={PREVIEW_NAV}
      slots={{}}
      shell={shell}
      pathname={path}
      search={search}
      onNavigate={onNavigate}
      flat={narrow}
      find={false}
      // A static scope header, as the demo's: no live account or project controls.
      header={
        <>
          <SidebarOrgRow>
            <ForgeBrand />
          </SidebarOrgRow>
          <SidebarProjectLabel name={DEMO_PROJECT} />
        </>
      }
    />
  );
}

/** The app's frame and the console's nav around a router's console routes (ConsoleFixturePreview). */
export function PreviewShellFrame({ children }: { children: ReactNode }) {
  return (
    <SessionProvider auth={PREVIEW_SESSION}>
      <AppShellFrame nav={PreviewNav}>{children}</AppShellFrame>
    </SessionProvider>
  );
}

const SHOP = shopPack();

const ENVS = DEMO_ENVS.map((env) => ({ name: env.name, recorded: true }));

export interface ConsolePreviewFrameProps {
  /** The console page the screen is: the nav's current row and the breadcrumb. */
  page: ConsolePage;
  /** The harness's own pickers, in a strip above the screen. */
  controls?: ReactNode;
  /** The bar's environment scope, when the screen reads it; otherwise the frame keeps its own. */
  env?: string;
  onEnvChange?: (env: string) => void;
  /** What serves the nav's reads (and the screen's): the shop fixture pack by default. */
  pack?: FixturePack;
  overlay?: FixturePack | readonly FixturePack[] | null;
  serve?: FixtureServeOptions;
  children: ReactNode;
}

/** The app's frame, the console's nav and the console's bar around one screen at a dev route of its own. */
export function ConsolePreviewFrame({ page, controls, env: controlledEnv, onEnvChange, pack = SHOP, overlay, serve = SHOP_SERVE, children }: ConsolePreviewFrameProps) {
  const [ownEnv, setOwnEnv] = useState(page.kind === "environment" ? page.env : "prod");
  const env = controlledEnv ?? ownEnv;
  return (
    <FixturePreview pack={pack} overlay={overlay} serve={serve}>
      <SessionProvider auth={PREVIEW_SESSION}>
        <PreviewPageContext.Provider value={page}>
          <AppShellFrame nav={PreviewNav} always>
            <ConsoleFrame project={DEMO_PROJECT} page={page} env={env} scope={{ long: ENVS, previews: [] }} onEnvChange={onEnvChange ?? setOwnEnv}>
              {controls && (
                <div className="mb-6 flex flex-wrap items-center gap-3 rounded-lg border border-dashed border-border px-3 py-2" data-testid="preview-controls">
                  {controls}
                </div>
              )}
              {children}
            </ConsoleFrame>
          </AppShellFrame>
        </PreviewPageContext.Provider>
      </SessionProvider>
    </FixturePreview>
  );
}
