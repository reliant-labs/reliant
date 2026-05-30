import { ArrowLeft, X, Settings, Activity, GitBranch } from "lucide-react";
import { Tooltip } from "../ui/Tooltip";
import { isDev } from "../../lib/constants";
import { openExternalLink } from "../../lib/open-link";
import { useTitleBarChrome } from "../../hooks/useTitleBarChrome";

interface WorkflowHeaderProps {
  onClose: () => void;
  onNavigateToSettings: () => void;
}

export function WorkflowHeader({
  onClose,
  onNavigateToSettings,
}: WorkflowHeaderProps) {
  const {
    isElectron,
    trafficLightPadding,
    dragRegionStyle,
    noDragRegionStyle,
  } = useTitleBarChrome({ collapsedPadding: "8px" });

  return (
    <header
      className={`h-12 border-b border-border/70 flex items-center bg-card/95 dense-ui select-none relative z-[100] shadow-sm shadow-black/5 ${isElectron ? "cursor-move" : ""}`}
      style={dragRegionStyle}
    >
      {/* Left side - back/close button. Padded to clear the macOS traffic
          lights in Electron (only on mac, only out of fullscreen). */}
      <div
        className="flex items-center transition-[padding] duration-200 ease-in-out"
        style={{ paddingLeft: trafficLightPadding }}
      >
        <div
          className="flex items-center cursor-default"
          style={noDragRegionStyle}
        >
          {/* Close button — X in Electron, back arrow on web */}
          <Tooltip
            content={isElectron ? "Close Workflow Builder (Esc)" : "Back to app (Esc)"}
            placement="bottom"
            delay={300}
          >
            <button
              onClick={onClose}
              className="inline-flex h-9 items-center gap-2 rounded-md px-3 text-sm font-medium text-foreground transition-colors hover:bg-muted/70"
              aria-label={isElectron ? "Close Workflow Builder" : "Back to app"}
            >
              {isElectron ? <X className="w-4 h-4" /> : <ArrowLeft className="w-4 h-4" />}
              <span>{isElectron ? "Close" : "Back"}</span>
            </button>
          </Tooltip>
        </div>
      </div>

      {/* Center - Title */}
      <div className="absolute left-1/2 -translate-x-1/2 flex items-center justify-center">
        <div
          className="inline-flex items-center gap-2 rounded-full border border-border/70 bg-background/70 px-3 py-1 text-xs font-semibold uppercase tracking-[0.14em] text-muted-foreground shadow-sm"
          style={noDragRegionStyle}
        >
          <GitBranch className="h-3.5 w-3.5 text-primary" />
          Workflow Builder
        </div>
      </div>

      {/* Draggable filler */}
      <div className="flex-1" style={dragRegionStyle} />

      {/* Right side - secondary controls */}
      <div
        className="flex items-center gap-1 pr-2 cursor-default"
        style={noDragRegionStyle}
      >
        {/* Dev-only: Temporal UI button */}
        {isDev && (
          <Tooltip content="Open Temporal UI" placement="bottom" delay={300}>
            <button
              onClick={() => {
                const temporalUIPort = window.RELIANT_CONFIG?.temporalUIPort || 8233;
                const temporalUIUrl = `http://localhost:${temporalUIPort}`;
                void openExternalLink(temporalUIUrl);
              }}
              className="header-icon-btn p-1.5 rounded text-xs transition-colors"
              aria-label="Open Temporal UI"
            >
              <Activity className="w-4 h-4" />
            </button>
          </Tooltip>
        )}

        {/* Settings button */}
        <Tooltip content="Settings" placement="bottom" delay={300}>
          <button
            onClick={onNavigateToSettings}
            className="header-icon-btn p-1.5 rounded text-xs transition-colors"
            aria-label="Settings"
          >
            <Settings className="w-4 h-4" />
          </button>
        </Tooltip>
      </div>
    </header>
  );
}