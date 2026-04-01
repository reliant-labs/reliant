import { AlertCircle, Pause, Play, RefreshCw, Settings, SlidersHorizontal, Trash2 } from "lucide-react";
import { useEffect, useRef, useState } from "react";
import { ConfigScope } from "../../api/mcp-grpc";
import { cn } from "../../lib/utils";
import { Badge } from "../ui/Badge";
import { Button } from "../ui/Button";
import { MCPAvatar } from "./mcpAvatar";

const SCOPE_LABELS: Record<ConfigScope, string> = {
  [ConfigScope.UNSPECIFIED]: "Project",
  [ConfigScope.GLOBAL]: "Global",
  [ConfigScope.PROJECT]: "Project",
  [ConfigScope.PROJECT_LOCAL]: "Project (Local)",
};

interface MCPServerListItemProps {
  name: string;
  displayName?: string;
  connected: boolean;
  enabled?: boolean;
  scope?: ConfigScope;
  toolCount: number;
  resourcesEnabled?: boolean;
  promptsEnabled?: boolean;
  error?: string;
  needsSetup?: boolean;
  iconSrc?: string;
  avatarSeedParts?: string[];
  onConfigure: () => void;
  onRestart: () => void;
  onToggleEnabled?: (enabled: boolean) => void;
  onMoveScope?: (scope: ConfigScope) => void;
  onViewTools?: () => void;
  onRemove: () => void;
  disabled?: boolean;
  loadingAction?: "configure" | "restart" | "remove" | "toggle" | "scope" | "tools" | null;
  configureDisabled?: boolean;
}

export function MCPServerListItem({
  name,
  displayName,
  connected,
  enabled = true,
  scope = ConfigScope.PROJECT,
  toolCount,
  resourcesEnabled = false,
  promptsEnabled = false,
  error,
  needsSetup = false,
  iconSrc,
  avatarSeedParts,
  onConfigure,
  onRestart,
  onToggleEnabled,
  onMoveScope,
  onViewTools,
  onRemove,
  disabled = false,
  loadingAction = null,
  configureDisabled = false,
}: MCPServerListItemProps) {
  const [settingsOpen, setSettingsOpen] = useState(false);
  const settingsRef = useRef<HTMLDivElement>(null);

  const title = displayName || name;
  const statusText = !enabled ? "Inactive" : connected ? "Active" : "Disconnected";
  const statusVariant: "warning" | "success" | "destructive" = !enabled
    ? "warning"
    : connected
      ? "success"
      : "destructive";
  const scopeLabel = SCOPE_LABELS[scope] ?? "Project";

  useEffect(() => {
    if (!settingsOpen) return;

    const handleClickOutside = (event: MouseEvent) => {
      if (settingsRef.current && !settingsRef.current.contains(event.target as Node)) {
        setSettingsOpen(false);
      }
    };

    const handleEscape = (event: KeyboardEvent) => {
      if (event.key === "Escape") {
        setSettingsOpen(false);
      }
    };

    document.addEventListener("mousedown", handleClickOutside);
    document.addEventListener("keydown", handleEscape);

    return () => {
      document.removeEventListener("mousedown", handleClickOutside);
      document.removeEventListener("keydown", handleEscape);
    };
  }, [settingsOpen]);

  const closeAndRun = (action: () => void) => {
    setSettingsOpen(false);
    action();
  };

  return (
    <div
      className={cn(
        "group rounded-2xl border border-border/70 bg-card/70 hover:bg-card transition-colors px-4 py-3",
        disabled && "opacity-70"
      )}
    >
      <div className="flex items-start justify-between gap-3">
        <div className="min-w-0 flex flex-1 items-start gap-3">
          <MCPAvatar
            name={title}
            iconSrc={iconSrc}
            colorSeedParts={avatarSeedParts ?? [name, title, scopeLabel]}
            alt={`${title} icon`}
          />

          <div className="min-w-0">
            <div className="flex min-w-0 items-center gap-1.5 overflow-hidden whitespace-nowrap">
              <span className="min-w-0 truncate font-medium text-sm">{title}</span>
              <Badge variant={statusVariant} size="sm" className="shrink-0 whitespace-nowrap">
                {statusText}
              </Badge>
              <Badge variant="outline" size="sm" className="shrink-0 max-w-[8rem] whitespace-nowrap truncate">
                {scopeLabel}
              </Badge>
            </div>

            <div className="mt-1 flex min-h-5 items-center gap-2 overflow-hidden text-xs text-muted-foreground">
              <button
                type="button"
                onClick={onViewTools}
                disabled={!onViewTools || disabled || loadingAction === "tools"}
                className={cn(
                  "shrink-0 text-xs text-muted-foreground underline-offset-2",
                  onViewTools && !(disabled || loadingAction === "tools")
                    ? "cursor-pointer hover:underline"
                    : "cursor-not-allowed opacity-70"
                )}
              >
                {loadingAction === "tools" ? "Loading tools…" : `${toolCount} tools`}
              </button>
              {resourcesEnabled && (
                <Badge variant="outline" size="sm" className="shrink-0 whitespace-nowrap">
                  Resources
                </Badge>
              )}
              {promptsEnabled && (
                <Badge variant="outline" size="sm" className="shrink-0 whitespace-nowrap">
                  Prompts
                </Badge>
              )}
            </div>

            {error && (
              <div className="mt-2 inline-flex max-w-full items-start gap-1.5 rounded-md border border-destructive/20 bg-destructive/5 px-2 py-1 text-xs text-destructive">
                <AlertCircle className="mt-0.5 h-3.5 w-3.5 shrink-0" />
                <span className="line-clamp-2 break-words">{error}</span>
              </div>
            )}
          </div>
        </div>

        <div className="relative shrink-0" ref={settingsRef}>
          <Button
            type="button"
            variant="ghost"
            size="sm"
            className="h-8 w-8 p-0 opacity-100 sm:opacity-0 sm:group-hover:opacity-100 sm:focus-visible:opacity-100 transition-opacity"
            onClick={() => setSettingsOpen((prev) => !prev)}
            aria-label={`Server settings for ${title}`}
            aria-expanded={settingsOpen}
            aria-haspopup="menu"
            disabled={disabled}
          >
            <Settings className="h-4 w-4" />
          </Button>

          {settingsOpen && (
            <div
              role="menu"
              className="absolute right-0 top-10 z-30 w-[min(20rem,calc(100vw-3rem))] rounded-lg border border-border bg-popover p-3 text-popover-foreground shadow-xl"
            >
              <div className="space-y-3">
                <div className="space-y-1.5">
                  <label className="text-xs text-muted-foreground">Install location</label>
                  <select
                    value={scope}
                    onChange={(event) => onMoveScope?.(Number(event.target.value) as ConfigScope)}
                    disabled={disabled || loadingAction === "scope"}
                    className="h-8 w-full rounded-md border border-input bg-background px-2 text-xs text-foreground"
                    aria-label={`Change install scope for ${title}`}
                  >
                    <option value={ConfigScope.GLOBAL}>Global</option>
                    <option value={ConfigScope.PROJECT}>Project</option>
                    <option value={ConfigScope.PROJECT_LOCAL}>Project (Local)</option>
                  </select>
                </div>

                <div className="grid grid-cols-1 gap-2">
                  <Button
                    type="button"
                    size="sm"
                    variant={needsSetup ? "default" : "outline"}
                    className="justify-start"
                    leftIcon={<SlidersHorizontal className="h-3.5 w-3.5" />}
                    onClick={() => closeAndRun(onConfigure)}
                    disabled={disabled || configureDisabled || loadingAction === "scope"}
                    loading={loadingAction === "configure"}
                  >
                    {needsSetup ? "Complete Setup" : "Configure"}
                  </Button>

                  <Button
                    type="button"
                    size="sm"
                    variant="outline"
                    className="justify-start"
                    leftIcon={enabled ? <Pause className="h-3.5 w-3.5" /> : <Play className="h-3.5 w-3.5" />}
                    onClick={() => closeAndRun(() => onToggleEnabled?.(!enabled))}
                    disabled={disabled || !onToggleEnabled || loadingAction === "scope"}
                    loading={loadingAction === "toggle"}
                  >
                    {enabled ? "Disable" : "Enable"}
                  </Button>

                  <Button
                    type="button"
                    size="sm"
                    variant="outline"
                    className="justify-start"
                    leftIcon={<RefreshCw className="h-3.5 w-3.5" />}
                    onClick={() => closeAndRun(onRestart)}
                    disabled={disabled || !enabled}
                    loading={loadingAction === "restart"}
                  >
                    Restart
                  </Button>

                  <Button
                    type="button"
                    size="sm"
                    variant="outline"
                    className="justify-start"
                    leftIcon={<Trash2 className="h-3.5 w-3.5" />}
                    onClick={() => closeAndRun(onRemove)}
                    disabled={disabled}
                    loading={loadingAction === "remove"}
                  >
                    Remove
                  </Button>
                </div>
              </div>
            </div>
          )}
        </div>
      </div>
    </div>
  );
}
