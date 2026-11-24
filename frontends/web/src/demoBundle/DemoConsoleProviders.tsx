// Copyright (c) 2025 Reliant Labs

/**
 * What the forge console's pages read from context in the demo shell:
 * demoShellRoutes.tsx wraps its `_authenticated` layout in it, and the demo's
 * own pages (/forge/demo, DemoConsole) are inside it too, with their `section`:
 *
 *  - the Observability views read nothing from here: the console's REAL
 *    client reads the shop fixture pack over the demo's transport
 *    (demoShellBoot.ts), read-only there, observability usage included (the
 *    pack answers it as a control plane without billing would, so the usage
 *    panels hide rather than fail);
 *  - "Ask Reliant" is in handoff mode: a sheet with `forge …` commands to
 *    copy, never a chat.
 */

import type { ReactNode } from "react";

import { DemoBrowserProviders } from "@/components/Forge/Demo/DemoBrowserProviders";
import { DemoNavExtras } from "@/components/Forge/Demo/DemoNavExtras";
import type { DemoSection } from "@/components/Forge/Demo/demoPath";
import { AskAgentModeProvider } from "@/components/Forge/Observability/AskReliant/askAgentMode";

export function DemoConsoleProviders({ section, children }: { section?: DemoSection; children: ReactNode }) {
  return (
    <AskAgentModeProvider mode="handoff" cli="forge" demo>
      <DemoBrowserProviders>
        <DemoNavExtras section={section}>{children}</DemoNavExtras>
      </DemoBrowserProviders>
    </AskAgentModeProvider>
  );
}
