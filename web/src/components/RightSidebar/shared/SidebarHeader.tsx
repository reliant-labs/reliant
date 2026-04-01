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
        "flex items-center gap-2 px-3 py-2 border-b border-border bg-background/50",
        className
      )}
    >
      {/* Title (e.g. project name) on the left */}
      {title ? (
        <div className="flex-1 min-w-0 truncate text-sm font-medium text-foreground/80 uppercase mr-3">
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
          "header-icon-btn p-1.5 rounded text-muted-foreground hover:text-foreground transition-colors",
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
