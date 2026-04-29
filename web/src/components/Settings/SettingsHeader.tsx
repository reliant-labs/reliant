import { X, Activity, Settings } from "lucide-react";
import { Tooltip } from "../ui/Tooltip";
import { useState, useEffect } from "react";
import { isDev } from "../../lib/constants";
import { openExternalLink } from "../../lib/open-link";

interface SettingsHeaderProps {
  onClose: () => void;
}

export function SettingsHeader({ onClose }: SettingsHeaderProps) {
  const isMac = window.electronAPI?.platform === "darwin";
  const [isFullscreen, setIsFullscreen] = useState(false);

  // Track fullscreen state like main Header does
  useEffect(() => {
    const checkFullscreenStatus = async () => {
      if (window.electronAPI?.getFullscreenStatus) {
        const isFS = await window.electronAPI.getFullscreenStatus();
        setIsFullscreen(isFS);
      }
    };

    checkFullscreenStatus();
    window.addEventListener("resize", checkFullscreenStatus);

    let unsubscribe: (() => void) | undefined;
    if (window.electronAPI?.onFullscreenChanged) {
      unsubscribe = window.electronAPI.onFullscreenChanged((fs: boolean) => {
        setIsFullscreen(fs);
      });
    }

    return () => {
      window.removeEventListener("resize", checkFullscreenStatus);
      if (unsubscribe) unsubscribe();
    };
  }, []);

  return (
    <header
      className="relative z-[100] flex h-12 cursor-move select-none items-center border-b border-border/60 bg-card dense-ui"
      style={
        {
          WebkitAppRegion: "drag",
          WebkitUserSelect: "none",
          userSelect: "none",
        } as React.CSSProperties
      }
    >
      {/* Left side - Worktree Dev Indicator */}
      <div
        className="flex flex-1 items-center gap-2 transition-[padding] duration-200 ease-in-out"
        style={{ paddingLeft: !isFullscreen && isMac ? "80px" : "12px" }}
      >
        <div className="flex h-7 w-7 items-center justify-center rounded-md border border-border/50 bg-background/70 text-muted-foreground">
          <Settings className="h-3.5 w-3.5" />
        </div>
        <div className="min-w-0">
          <div className="truncate text-sm font-semibold text-foreground">Settings</div>
          <div className="truncate text-[11px] text-muted-foreground">
            Preferences and system configuration
          </div>
        </div>
      </div>

      {/* Right side - controls */}
      <div className="flex flex-1 items-center justify-end">
        {/* Draggable spacer */}
        <div
          className="min-w-4 flex-1"
          style={{ WebkitAppRegion: "drag" } as React.CSSProperties}
        />

        {/* Control buttons (not draggable) */}
        <div
          className="flex cursor-default items-center gap-1 pr-2"
          style={{ WebkitAppRegion: "no-drag" } as React.CSSProperties}
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

          {/* Close button */}
          <Tooltip content="Close Settings (Esc)" placement="bottom" delay={300}>
            <button
              onClick={onClose}
              className="rounded-md p-1.5 text-xs text-muted-foreground transition-colors hover:bg-muted/70 hover:text-foreground"
              aria-label="Close Settings"
            >
              <X className="h-4 w-4" />
            </button>
          </Tooltip>
        </div>
      </div>
    </header>
  );
}