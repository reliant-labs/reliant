// Copyright (c) 2026 Reliant Labs

/**
 * CONNECTORS (U6d, prototype): /connectors and /o/<org>/connectors, the
 * Agents area's Connectors row. Everything agents reach beyond their sandbox,
 * on one page with a tab each (owner: "bring out the connectors tab and maybe
 * MCP, or put them on the same page"):
 *
 *   Integrations  the accounts automations and agents act through (Slack,
 *                 Gmail, Twilio, HTTP, GitHub): Settings › Integrations' body
 *   MCP servers   the tools agents call: Settings › MCP servers' body
 *
 * The tab is the URL's `?tab=` (lib/paths connectorsPath), so Back, refresh
 * and a shared link all reopen it. The bodies are the Settings sections
 * themselves, not copies; their titles step down a rung under this page's
 * (SettingsHeadingLevelContext). Their Settings URLs keep working.
 */

import { useNavigate, useSearch } from "@tanstack/react-router";

import PageHeader from "@/components/forge-ui/page_header";
import Tabs from "@/components/forge-ui/tabs";
import { connectorsPath, type ConnectorsTab } from "@/lib/paths";

import { AreaShell } from "../Layout/AreaShell";
import { IntegrationsSettings } from "../Settings/IntegrationsSettings";
import { MCPSettings } from "../Settings/MCPSettings";
import { SettingsHeadingLevelContext } from "../Settings/SettingsPageHeader";

const TABS: { id: ConnectorsTab; label: string }[] = [
  { id: "integrations", label: "Integrations" },
  { id: "mcp", label: "MCP servers" },
];

/** The `?tab=` value as a tab: anything unknown is Integrations. */
export function connectorsTabOf(value: unknown): ConnectorsTab {
  return value === "mcp" ? "mcp" : "integrations";
}

/** The route's page (routes.tsx): the view in the app's area chrome. */
export function ConnectorsPage() {
  return (
    <AreaShell areaPath={connectorsPath()} areaLabel="Connectors" areaNoun="connectors" learn="connectors">
      <ConnectorsView />
    </AreaShell>
  );
}

export function ConnectorsView() {
  const navigate = useNavigate();
  const search = useSearch({ strict: false }) as Record<string, unknown>;
  const tab = connectorsTabOf(search.tab);
  const setTab = (next: ConnectorsTab) =>
    void navigate({
      to: ".",
      search: (prev: Record<string, unknown>) => ({ ...prev, tab: next === "integrations" ? undefined : next }),
    } as never);

  return (
    <div className="space-y-6" data-testid="connectors-page" data-tab={tab}>
      <PageHeader
        title="Connectors"
        meta={<span>What your agents can reach beyond their sandbox: the accounts they act through, and the tools they call.</span>}
        className="mb-0"
      />
      <Tabs tabs={TABS} activeTab={tab} onChange={(id) => setTab(connectorsTabOf(id))} ariaLabel="Connectors" />
      <SettingsHeadingLevelContext.Provider value={2}>
        <div role="tabpanel" aria-label={TABS.find((t) => t.id === tab)?.label}>
          {tab === "mcp" ? <MCPSettings /> : <IntegrationsSettings />}
        </div>
      </SettingsHeadingLevelContext.Provider>
    </div>
  );
}
