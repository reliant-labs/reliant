import { X, Settings, Activity, GitBranch } from "lucide-react";
import { Tooltip } from "../ui/Tooltip";
import { useState, useEffect } from "react";
import { isDev } from "../../lib/constants";
import { openExternalLink } from "../../lib/open-link";

interface WorkflowHeaderProps {
  onClose: () => void;
  onNavigateToSettings: () => void;
}

export function WorkflowHeader({
  onClose,
  onNavigateToSettings,
}: WorkflowHeaderProps) {
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
    window.addEventListener('resize', checkFullscreenStatus);

    let unsubscribe: (() => void) | undefined;
    if (window.electronAPI?.onFullscreenChanged) {
      unsubscribe = window.electronAPI.onFullscreenChanged((fs: boolean) => {
        setIsFullscreen(fs);
      });
    }

    return () => {
      window.removeEventListener('resize', checkFullscreenStatus);
      if (unsubscribe) unsubscribe();
    };
  }, []);

  return (
    <header
      className="h-12 border-b border-border/70 flex items-center bg-card/95 dense-ui select-none cursor-move relative z-[100] shadow-sm shadow-black/5"
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
        style={{ paddingLeft: !isFullscreen && isMac ? '80px' : '12px' }}
      >
      </div>

      {/* Center - Title */}
      <div className="absolute left-1/2 -translate-x-1/2 flex items-center justify-center">
        <div
          className="inline-flex items-center gap-2 rounded-full border border-border/70 bg-background/70 px-3 py-1 text-xs font-semibold uppercase tracking-[0.14em] text-muted-foreground shadow-sm"
          style={{ WebkitAppRegion: "no-drag" } as React.CSSProperties}
        >
          <GitBranch className="h-3.5 w-3.5 text-primary" />
          Workflow Builder
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
          <Tooltip content="Close Workflow Builder (Esc)" placement="bottom" delay={300}>
            <button
              onClick={onClose}
              className="header-icon-btn p-1.5 rounded text-xs transition-colors"
              aria-label="Close Workflow Builder"
            >
              <X className="w-4 h-4" />
            </button>
          </Tooltip>

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
      </div>
    </header>
  );
}