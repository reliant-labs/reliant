// Copyright (c) 2025 Reliant Labs

/**
 * What the forge console's pages read from context in the demo shell
 * (demoShellRoutes.tsx wraps its `_authenticated` layout in it), the same
 * providers DemoConsole gives its own panel:
 *
 *  - the Observability views read `useObservabilityApi()`, not the transport:
 *    the demo's API (Forge/Demo/demoData/demoApi.ts: the sample app on the
 *    console's real client where the scenario serves it, fixtures elsewhere,
 *    read-only either way);
 *  - observability usage is answered as an older control plane would (not
 *    implemented), so the usage panels hide rather than fail;
 *  - "Ask Reliant" is in handoff mode: a sheet with `forge …` commands to
 *    copy, never a chat.
 */

import { useMemo, type ReactNode } from "react";
import { Code, ConnectError } from "@connectrpc/connect";

import { createDemoApi } from "@/components/Forge/Demo/demoData/demoApi";
import { DemoBrowserProviders } from "@/components/Forge/Demo/DemoBrowserProviders";
import { DemoNavExtras } from "@/components/Forge/Demo/DemoNavExtras";
import { AskAgentModeProvider } from "@/components/Forge/Observability/AskReliant/askAgentMode";
import { ObservabilityBillingApiContext } from "@/hooks/observability-billing-queries";
import { ObservabilityApiProvider } from "@/services/controlPlane/observability/ObservabilityApiContext";

const notInDemo = async () => {
  throw new ConnectError("Not in the demo", Code.Unimplemented);
};
const usageApi = {
  getObservabilityUsage: notInDemo,
  listObservabilityUsage: notInDemo,
  getObservabilityStorage: notInDemo,
  getObservabilityLimits: notInDemo,
  updateObservabilityLimits: notInDemo,
};

export function DemoConsoleProviders({ children }: { children: ReactNode }) {
  const api = useMemo(() => createDemoApi(), []);
  return (
    <AskAgentModeProvider mode="handoff" cli="forge" demo>
      <DemoBrowserProviders>
        <ObservabilityApiProvider api={api}>
          <ObservabilityBillingApiContext.Provider value={usageApi}>
            <DemoNavExtras>{children}</DemoNavExtras>
          </ObservabilityBillingApiContext.Provider>
        </ObservabilityApiProvider>
      </DemoBrowserProviders>
    </AskAgentModeProvider>
  );
}
