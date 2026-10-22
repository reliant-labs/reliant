import { ArrowLeft, X, Activity } from "lucide-react";
import { Tooltip } from "../ui/Tooltip";
import { isDev } from "../../lib/constants";
import { openExternalLink } from "../../lib/open-link";
import { useTitleBarChrome } from "../../hooks/useTitleBarChrome";

interface SettingsHeaderProps {
  onClose: () => void;
}

export function SettingsHeader({ onClose }: SettingsHeaderProps) {
  const {
    isElectron,
    trafficLightPadding,
    dragRegionStyle,
    noDragRegionStyle,
  } = useTitleBarChrome({ collapsedPadding: "8px" });

  return (
    <header
      className={`relative z-[100] flex h-12 select-none items-center border-b border-border/60 bg-card dense-ui ${isElectron ? "cursor-move" : ""}`}
      style={dragRegionStyle}
    >
      {/* Left side - back/close button. Padded to clear the macOS traffic
          lights in Electron (only on mac, only out of fullscreen). */}
      <div
        className="flex items-center transition-[padding] duration-200 ease-in-out"
        style={{ paddingLeft: trafficLightPadding }}
      >
        <div
          className="flex cursor-default items-center"
          style={noDragRegionStyle}
        >
          {/* Close button — X in Electron (matches window controls aesthetic),
              back arrow on web (no window-close semantics in a browser tab) */}
          <Tooltip
            content={isElectron ? "Close Settings (Esc)" : "Back to app (Esc)"}
            placement="bottom"
            delay={300}
          >
            <button
              onClick={onClose}
              className="inline-flex h-9 items-center gap-2 rounded-md px-3 text-sm font-medium text-foreground transition-colors hover:bg-muted/70"
              aria-label={isElectron ? "Close Settings" : "Back to app"}
            >
              {isElectron ? <X className="h-4 w-4" /> : <ArrowLeft className="h-4 w-4" />}
              <span>{isElectron ? "Close" : "Back"}</span>
            </button>
          </Tooltip>
        </div>
      </div>

      {/* Draggable filler */}
      <div className="flex-1" style={dragRegionStyle} />

      {/* Right side - secondary controls */}
      <div
        className="flex cursor-default items-center gap-1 pr-2"
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
              className="rounded-md p-1.5 text-xs text-muted-foreground transition-colors hover:bg-muted/70 hover:text-foreground"
              aria-label="Open Temporal UI"
            >
              <Activity className="h-4 w-4" />
            </button>
          </Tooltip>
        )}
      </div>
    </header>
  );
}