// Copyright (c) 2025 Reliant Labs

/**
 * The public demo's additions to the console nav (Console/consoleNavExtras):
 *
 *  - "How it was built", the recorded forge run (`/forge/demo?section=built`),
 *    as the first row, and the page's own breadcrumb when it is open;
 *  - the environments in promotion order (dev, staging, prod), on every page
 *    of either console the demo mounts;
 *  - on a phone, the tour, Install Forge and the telemetry notice in the
 *    drawer, so the row under the header is one line (DemoScreenActions).
 */

import { useMemo, type ReactNode } from "react";
import { Hammer } from "lucide-react";

import { ConsoleNavExtrasProvider } from "../Console/consoleNavExtras";
import { DemoDrawerActions } from "./DemoScreenActions";
import { DEMO_SECTION_BUILT, FORGE_DEMO_PATH } from "./demoPath";

/** The build page's one name: nav row, breadcrumb, banner and tour step. */
export const HOW_BUILT_LABEL = "How it was built";
export const HOW_BUILT_HREF = `${FORGE_DEMO_PATH}?section=${DEMO_SECTION_BUILT}`;
export const DEMO_ENV_ORDER = ["dev", "staging", "prod"] as const;

export function DemoNavExtras({ builtActive = false, children }: { builtActive?: boolean; children: ReactNode }) {
  const value = useMemo(
    () => ({
      lead: { label: HOW_BUILT_LABEL, href: HOW_BUILT_HREF, icon: <Hammer className="h-4 w-4" aria-hidden="true" />, active: builtActive },
      envOrder: DEMO_ENV_ORDER,
      drawerAppendix: <DemoDrawerActions />,
    }),
    [builtActive],
  );
  return <ConsoleNavExtrasProvider value={value}>{children}</ConsoleNavExtrasProvider>;
}
