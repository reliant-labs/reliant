import { Plus } from "lucide-react";
import { type MCPServer, type RecommendedServer, ConfigScope } from "../../api/mcp-grpc";
import { Button } from "../ui/Button";
import { Card } from "../ui/Card";
import { MCPServerListItem } from "./MCPServerListItem";

interface MCPCustomTabProps {
  customServers: MCPServer[];
  recommendedServers: RecommendedServer[];
  onAddCustom: () => void;
  onConfigure: (server: MCPServer) => void | Promise<void>;
  onRestart: (name: string) => void | Promise<void>;
  onToggleEnabled: (name: string, enabled: boolean) => void | Promise<void>;
  onMoveScope: (name: string, scope: ConfigScope) => void | Promise<void>;
  onViewTools: (name: string) => void | Promise<void>;
  onRemove: (name: string) => void | Promise<void>;
  loadingActions?: Record<string, "configure" | "restart" | "remove" | "toggle" | "scope" | "tools" | null>;
}

export function MCPCustomTab({
  customServers,
  recommendedServers,
  onAddCustom,
  onConfigure,
  onRestart,
  onToggleEnabled,
  onMoveScope,
  onViewTools,
  onRemove,
  loadingActions = {},
}: MCPCustomTabProps) {
  return (
    <div className="space-y-4">
      <Card className="border-border/70 bg-card/80 shadow-sm">
        <div className="p-5 flex items-center justify-between gap-3">
          <div>
            <h4 className="text-base font-semibold">Add Custom MCP Server</h4>
            <p className="text-sm text-muted-foreground">
              Configure any stdio or SSE MCP server with command, args, env, and scope.
            </p>
          </div>
          <Button variant="default" size="sm" onClick={onAddCustom}>
            <span className="inline-flex items-center gap-1 whitespace-nowrap">
              <Plus className="w-4 h-4" />
              Add Custom
            </span>
          </Button>
        </div>
      </Card>

      {customServers.length === 0 ? (
        <Card className="border-border/70 bg-card/70">
          <div className="p-6 text-center">
            <h5 className="font-medium">No custom servers yet</h5>
            <p className="text-sm text-muted-foreground mt-1">
              Add your first custom MCP server to connect tools outside the marketplace list.
            </p>
          </div>
        </Card>
      ) : (
        <div className="grid grid-cols-1 lg:grid-cols-2 gap-2.5">
          {customServers.map((server) => {
            const recommendedServer = recommendedServers.find((r) => r.name === server.name);
            return (
              <MCPServerListItem
                key={server.name}
                name={server.name}
                displayName={server.serverInfo?.name}
                connected={server.connected}
                enabled={server.enabled}
                scope={server.scope}
                toolCount={server.toolCount}
                resourcesEnabled={server.resourcesEnabled}
                promptsEnabled={server.promptsEnabled}
                error={server.lastError}
                needsSetup={Boolean(recommendedServer?.setupRequired && server.enabled && !server.connected)}
                avatarSeedParts={[server.name, server.serverInfo?.name || "", "custom"]}
                onConfigure={() => onConfigure(server)}
                onRestart={() => onRestart(server.name)}
                onToggleEnabled={(enabled) => onToggleEnabled(server.name, enabled)}
                onMoveScope={(scope) => onMoveScope(server.name, scope)}
                onViewTools={() => onViewTools(server.name)}
                onRemove={() => onRemove(server.name)}
                configureDisabled={!recommendedServer || !recommendedServer.configFields?.length}
                loadingAction={loadingActions[server.name] ?? null}
              />
            );
          })}
        </div>
      )}
    </div>
  );
}
