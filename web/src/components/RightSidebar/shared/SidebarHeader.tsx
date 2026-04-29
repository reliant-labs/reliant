import type { ReactNode } from "react";
import { cn } from "../../../lib/utils";
import { Tooltip } from "../../ui/Tooltip";

interface SidebarHeaderProps {
  /** Optional title or project name on the left */
  title?: ReactNode;
  /** Optional status badge or indicator */
  statusBadge?: ReactNode;
  /** Action buttons to display on the right */
  actions?: ReactNode;
  /** Search input component to display inline */
  searchInput?: ReactNode;
  /** Additional CSS classes */
  className?: string;
}

/**
 * Unified header component for right sidebar tabs.
 * Provides consistent layout for search input and action buttons.
 */
export function SidebarHeader({
  title,
  statusBadge,
  actions,
  searchInput,
  className,
}: SidebarHeaderProps) {
  return (
    <div
      className={cn(
        "m-2 flex items-center gap-2 rounded-[10px] border border-border bg-card/95 px-3 py-2 shadow-sm",
        className
      )}
    >
      {/* Title (e.g. project name) on the left */}
      {title ? (
        <div className="mr-3 min-w-0 flex-1 truncate text-[11px] font-bold uppercase tracking-[0.06em] text-muted-foreground/80">
          {title}
        </div>
      ) : searchInput ? (
        <div className="flex-1 min-w-0">
          {searchInput}
        </div>
      ) : (
        /* Spacer to push items to the right when no search input and not centered */
        !className?.includes('justify-center') && <div className="flex-1" />
      )}

      {/* Status badges */}
      {statusBadge && (
        <div className="flex items-center gap-1 flex-shrink-0">
          {statusBadge}
        </div>
      )}

      {/* Action buttons */}
      {actions && (
        <div className="flex items-center gap-0.5 flex-shrink-0">{actions}</div>
      )}
    </div>
  );
}

interface SidebarHeaderButtonProps {
  icon: ReactNode;
  onClick: () => void;
  tooltip: string;
  disabled?: boolean;
  className?: string;
}

/**
 * Standardized action button for sidebar headers.
 * Use this for consistent button styling across all sidebar tabs.
 */
export function SidebarHeaderButton({
  icon,
  onClick,
  tooltip,
  disabled,
  className,
}: SidebarHeaderButtonProps) {
  return (
    <Tooltip content={tooltip} placement="bottom">
      <button
        onClick={onClick}
        disabled={disabled}
        className={cn(
          "header-icon-btn rounded-md p-1.5 text-muted-foreground transition-colors hover:bg-muted hover:text-foreground",
          "disabled:opacity-50 disabled:cursor-not-allowed",
          className
        )}
        aria-label={tooltip}
      >
        {icon}
      </button>
    </Tooltip>
  );
}