import { Search } from "lucide-react";
import { useCallback, useMemo, useState } from "react";
import { type MCPServer, type RecommendedServer, ConfigScope } from "../../api/mcp-grpc";
import { Button } from "../ui/Button";
import { Input } from "../ui/Input";
import { MCPServerListItem } from "./MCPServerListItem";
import { MCPTabs } from "./MCPTabs";
import { getMcpIconUrl } from "./mcpAvatar";

type InstalledStatusFilter = "all" | "active" | "inactive" | "disconnected" | "needs-setup";

interface MCPInstalledTabProps {
  installedServers: MCPServer[];
  recommendedServers: RecommendedServer[];
  onConfigure: (server: MCPServer) => void;
  onRestart: (name: string) => void;
  onToggleEnabled: (name: string, enabled: boolean) => void;
  onMoveScope: (name: string, scope: ConfigScope) => void;
  onViewTools: (name: string) => void;
  onRemove: (name: string) => void;
  onGoToDiscover: () => void;
  loadingActions?: Record<string, "configure" | "restart" | "remove" | "toggle" | "scope" | "tools" | null>;
}

export function MCPInstalledTab({
  installedServers,
  recommendedServers,
  onConfigure,
  onRestart,
  onToggleEnabled,
  onMoveScope,
  onViewTools,
  onRemove,
  onGoToDiscover,
  loadingActions = {},
}: MCPInstalledTabProps) {
  const [query, setQuery] = useState("");
  const [statusFilter, setStatusFilter] = useState<InstalledStatusFilter>("all");

  const recommendedByName = useMemo(() => {
    return new Map(recommendedServers.map((server) => [server.name, server]));
  }, [recommendedServers]);

  const hasSetupRequired = useCallback(
    (serverName: string): boolean => {
      const recommended = recommendedByName.get(serverName);
      return Boolean(recommended?.setupRequired);
    },
    [recommendedByName],
  );

  const normalizedQuery = query.trim().toLowerCase();

  const filteredServers = useMemo(() => {
    return installedServers.filter((server) => {
      const displayName = server.serverInfo?.name || "";
      const matchesSearch =
        normalizedQuery.length === 0 ||
        server.name.toLowerCase().includes(normalizedQuery) ||
        displayName.toLowerCase().includes(normalizedQuery);

      if (!matchesSearch) {
        return false;
      }

      const disconnected = server.enabled && !server.connected;
      const needsSetup = disconnected && hasSetupRequired(server.name);

      switch (statusFilter) {
        case "active":
          return server.enabled && server.connected;
        case "inactive":
          return !server.enabled;
        case "disconnected":
          return disconnected;
        case "needs-setup":
          return needsSetup;
        case "all":
        default:
          return true;
      }
    });
  }, [installedServers, normalizedQuery, statusFilter, hasSetupRequired]);

  const activeCount = useMemo(
    () => installedServers.filter((server) => server.enabled && server.connected).length,
    [installedServers],
  );

  const inactiveCount = useMemo(
    () => installedServers.filter((server) => !server.enabled).length,
    [installedServers],
  );

  const disconnectedCount = useMemo(
    () => installedServers.filter((server) => server.enabled && !server.connected).length,
    [installedServers],
  );

  const needsSetupCount = useMemo(
    () =>
      installedServers.filter(
        (server) => server.enabled && !server.connected && hasSetupRequired(server.name),
      ).length,
    [installedServers, hasSetupRequired],
  );

  const tabItems = useMemo(
    () => [
      { id: "all", label: "All", shortLabel: "All", count: installedServers.length },
      {
        id: "active",
        label: "Active",
        shortLabel: "On",
        count: activeCount,
      },
      {
        id: "inactive",
        label: "Inactive",
        shortLabel: "Off",
        count: inactiveCount,
      },
      {
        id: "disconnected",
        label: "Disconnected",
        shortLabel: "Down",
        count: disconnectedCount,
      },
      {
        id: "needs-setup",
        label: "Needs Setup",
        shortLabel: "Setup",
        count: needsSetupCount,
      },
    ],
    [installedServers.length, activeCount, inactiveCount, disconnectedCount, needsSetupCount],
  );

  return (
    <div className="space-y-4">
      <Input
        value={query}
        onChange={(event) => setQuery(event.target.value)}
        placeholder="Search installed servers"
        leftIcon={<Search className="h-4 w-4" />}
        aria-label="Search installed MCP servers"
      />

      <MCPTabs
        tabs={tabItems}
        activeTab={statusFilter}
        onTabChange={(tabId) => setStatusFilter(tabId as InstalledStatusFilter)}
      />

      <div
        role="tabpanel"
        id={`mcp-tabpanel-${statusFilter}`}
        aria-labelledby={`mcp-tab-${statusFilter}`}
        className="space-y-3"
      >
        {filteredServers.length > 0 ? (
          <div className="grid grid-cols-1 lg:grid-cols-2 gap-2.5">
            {filteredServers.map((server) => {
              const recommended = recommendedByName.get(server.name);
              const displayName = server.serverInfo?.name || recommended?.displayName || server.name;
              const needsSetup = server.enabled && !server.connected && Boolean(recommended?.setupRequired);

              return (
                <MCPServerListItem
                  key={server.name}
                  name={server.name}
                  displayName={displayName}
                  connected={server.connected}
                  enabled={server.enabled}
                  scope={server.scope}
                  toolCount={server.toolCount}
                  resourcesEnabled={server.resourcesEnabled}
                  promptsEnabled={server.promptsEnabled}
                  error={server.lastError}
                  needsSetup={needsSetup}
                  iconSrc={getMcpIconUrl(server.name)}
                  avatarSeedParts={[server.name, displayName, String(server.scope)]}
                  onConfigure={() => onConfigure(server)}
                  onRestart={() => onRestart(server.name)}
                  onToggleEnabled={(enabled) => onToggleEnabled(server.name, enabled)}
                  onMoveScope={(scope) => onMoveScope(server.name, scope)}
                  onViewTools={() => onViewTools(server.name)}
                  onRemove={() => onRemove(server.name)}
                  loadingAction={loadingActions[server.name] ?? null}
                />
              );
            })}
          </div>
        ) : (
          <div className="rounded-xl border border-dashed border-border/70 bg-muted/30 p-8 text-center">
            <h3 className="text-sm font-semibold text-foreground">No servers match your current view</h3>
            <p className="mt-1 text-sm text-muted-foreground">
              Try a different search or filter, or install a new MCP server from Discover.
            </p>
            <div className="mt-4">
              <Button type="button" size="sm" onClick={onGoToDiscover}>
                Go to Discover
              </Button>
            </div>
          </div>
        )}
      </div>
    </div>
  );
}
