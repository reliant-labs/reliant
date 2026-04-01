import { X, Activity } from "lucide-react";
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
      className="h-12 border-b border-border flex items-center bg-background dense-ui select-none cursor-move relative z-[100]"
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
        className="flex items-center flex-1 transition-[padding] duration-200 ease-in-out gap-1"
        style={{ paddingLeft: !isFullscreen && isMac ? "80px" : "12px" }}
      >
      </div>

      {/* Center - Title */}
      <div className="absolute left-1/2 -translate-x-1/2 flex items-center justify-center">
        <div
          className="text-sm font-medium text-foreground/80 px-4 py-1"
          style={{ WebkitAppRegion: "no-drag" } as React.CSSProperties}
        >
          Settings
        </div>
      </div>

      {/* Right side - controls */}
      <div className="flex items-center flex-1 justify-end">
        {/* Draggable spacer */}
        <div
          className="flex-1 min-w-4"
          style={{ WebkitAppRegion: "drag" } as React.CSSProperties}
        />

        {/* Control buttons (not draggable) */}
        <div
          className="flex items-center gap-1 pr-2 cursor-default"
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
                className="header-icon-btn p-1.5 rounded text-xs transition-colors"
                aria-label="Open Temporal UI"
              >
                <Activity className="w-4 h-4" />
              </button>
            </Tooltip>
          )}

          {/* Close button */}
          <Tooltip content="Close Settings (Esc)" placement="bottom" delay={300}>
            <button
              onClick={onClose}
              className="header-icon-btn p-1.5 rounded text-xs transition-colors"
              aria-label="Close Settings"
            >
              <X className="w-4 h-4" />
            </button>
          </Tooltip>
        </div>
      </div>
    </header>
  );
}