import { useMemo } from "react";
import { Download, RefreshCw } from "lucide-react";
import { type RecommendedServer } from "../../api/mcp-grpc";
import { Button } from "../ui/Button";
import { getMcpIconUrl, MCPAvatar } from "./mcpAvatar";

function ensureServerIconUrl(server: RecommendedServer): string | undefined {
  const icon = getMcpIconUrl(server.name);
  if (!icon) return undefined;

  // Serena logo source is encoded UTF-16 and not browser-safe as-is for inline SVG rendering.
  // Keep deterministic initials fallback until a UTF-8 asset is available.
  if (server.name.toLowerCase() === "serena") {
    return undefined;
  }

  return icon;
}

interface MCPDiscoverTabProps {
  recommendedServers: RecommendedServer[];
  installingServers: Set<string>;
  onInstall: (server: RecommendedServer) => void | Promise<void>;
}

const getActionLabel = (server: RecommendedServer) => {
  if (server.setupRequired) return "Set up & install";
  return "Install";
};



export function MCPDiscoverTab({ recommendedServers, installingServers, onInstall }: MCPDiscoverTabProps) {
  const discoverServers = useMemo(
    () => recommendedServers.filter((server) => !server.installed),
    [recommendedServers],
  );

  return (
    <div className="space-y-4">
      {discoverServers.length === 0 ? (
        <div className="rounded-xl border border-dashed border-border/70 bg-muted/30 p-8 text-center">
          <p className="text-sm text-muted-foreground">All recommended servers are already installed.</p>
        </div>
      ) : (
        <div className="grid grid-cols-1 md:grid-cols-2 gap-2.5">
          {discoverServers.map((server) => {
            const isInstalling = installingServers.has(server.name);
            const actionLabel = getActionLabel(server);
            const iconSrc = ensureServerIconUrl(server);

            return (
              <div
                key={server.name}
                className="group rounded-2xl border border-border/70 bg-card/50 hover:bg-card transition-colors px-4 py-3"
              >
                <div className="min-w-0 w-full flex items-start gap-3 text-left">
                  <MCPAvatar
                    name={server.displayName || server.name}
                    iconSrc={iconSrc}
                    colorSeedParts={[server.name, server.displayName, server.category]}
                    alt={`${server.displayName} icon`}
                  />

                  <div className="min-w-0 flex-1">
                    <div className="flex items-start justify-between gap-2">
                      <div className="min-w-0 flex-1">
                        <div className="flex min-w-0 items-center gap-1.5 overflow-hidden whitespace-nowrap">
                          <span className="min-w-0 truncate font-medium text-sm">{server.displayName}</span>
                        </div>
                        <p className="text-xs text-muted-foreground mt-1 line-clamp-2">
                          {server.description}
                        </p>
                      </div>

                      <Button
                        type="button"
                        variant="ghost"
                        size="sm"
                        className="h-8 w-8 p-0 opacity-100 sm:opacity-0 sm:group-hover:opacity-100 sm:focus-visible:opacity-100 transition-opacity shrink-0"
                        onClick={() => onInstall(server)}
                        disabled={isInstalling}
                        aria-label={`${actionLabel} ${server.displayName}`}
                      >
                        {isInstalling ? <RefreshCw className="h-4 w-4 animate-spin" /> : <Download className="h-4 w-4" />}
                      </Button>
                    </div>

                  </div>
                </div>
              </div>
            );
          })}
        </div>
      )}
    </div>
  );
}