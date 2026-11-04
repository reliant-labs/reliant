import { ArrowLeft, X, Activity } from "lucide-react";
import Tooltip from "../forge-ui/tooltip";
import { OrgSwitcher } from "../Orgs/OrgSwitcher";
import { cn } from "../../lib/utils";
import { isDev } from "../../lib/constants";
import { openExternalLink } from "../../lib/open-link";
import type { CSSProperties } from "react";

interface SettingsHeaderProps {
  onClose: () => void;
  isCollapsed: boolean;
  isElectron: boolean;
  dragRegionStyle: CSSProperties;
  noDragRegionStyle: CSSProperties;
}

/** The sidebar's unboxed title and always-reachable exit, not a second title bar. */
export function SettingsHeader({ onClose, isCollapsed, isElectron, dragRegionStyle, noDragRegionStyle }: SettingsHeaderProps) {
  return (
    <header className="flex shrink-0 select-none flex-wrap items-center gap-2 p-2" style={dragRegionStyle}>
      <Tooltip content={isElectron ? "Close Settings (Esc)" : "Back to app (Esc)"} side="right" delay={300}>
        <button
          type="button"
          onClick={onClose}
          style={noDragRegionStyle}
          className={cn(
            "inline-flex h-11 shrink-0 items-center justify-center gap-2 rounded-md text-sm font-medium text-foreground transition-colors hover:bg-muted/70 focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
            isCollapsed ? "w-11" : "px-3",
          )}
          aria-label={isElectron ? "Close Settings" : "Back to app"}
        >
          {isElectron ? <X className="h-4 w-4" aria-hidden="true" /> : <ArrowLeft className="h-4 w-4" aria-hidden="true" />}
          {!isCollapsed && <span>{isElectron ? "Close" : "Back"}</span>}
        </button>
      </Tooltip>
      {!isCollapsed && <span className="text-sm font-semibold text-foreground">Settings</span>}
      {isDev && !isCollapsed && (
        <Tooltip content="Open Temporal UI" side="bottom" delay={300}>
          <button
            type="button"
            aria-label="Open Temporal UI"
            style={noDragRegionStyle}
            onClick={() => {
              const port = window.RELIANT_CONFIG?.temporalUIPort || 8233;
              void openExternalLink(`http://localhost:${port}`);
            }}
            className="ml-auto rounded-md p-1.5 text-muted-foreground hover:bg-muted/70 hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
          >
            <Activity className="h-4 w-4" aria-hidden="true" />
          </button>
        </Tooltip>
      )}
      {/* Keep the current organization reachable when the sidebar becomes a rail. */}
      <div className="min-w-0 w-full overflow-hidden" style={noDragRegionStyle}>
        <OrgSwitcher />
      </div>
    </header>
  );
}