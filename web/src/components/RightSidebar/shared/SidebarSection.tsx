import type { ReactNode } from "react";
import { ChevronDown, ChevronRight } from "lucide-react";
import { cn } from "../../../lib/utils";

interface SidebarSectionProps {
  /** Section title */
  title: string;
  /** Item count to display as badge */
  count?: number;
  /** Optional icon before title */
  icon?: ReactNode;
  /** Whether section is expanded */
  isExpanded: boolean;
  /** Callback when expand/collapse is toggled */
  onToggle: () => void;
  /** Optional action buttons for the section header */
  actions?: ReactNode;
  /** Section content (shown when expanded) */
  children: ReactNode;
  /** Additional CSS classes for the container */
  className?: string;
  /** Additional CSS classes for the header */
  headerClassName?: string;
  /** If true, use the same hover background behavior as file rows */
  enableHeaderHoverBg?: boolean;
  /** Variant styling */
  variant?: "default" | "highlighted";
}

/**
 * Collapsible section component for sidebar lists.
 * Provides consistent expand/collapse behavior and styling.
 */
export function SidebarSection({
  title,
  count,
  icon,
  isExpanded,
  onToggle,
  actions,
  children,
  className,
  headerClassName,
  enableHeaderHoverBg = false,
  variant = "default",
}: SidebarSectionProps) {
  return (
    <div className={cn("mx-2 mb-2 overflow-hidden rounded-[10px] border border-border bg-card/95 shadow-sm", className)}>
      {/* Section header */}
      <div
        className={cn(
          "flex items-center border-b border-border/50",
          variant === "highlighted"
            ? "bg-primary/5 hover:bg-primary/10"
            : headerClassName
              ? undefined
              : "bg-muted/20 hover:bg-muted/40",
          "transition-colors",
          headerClassName
        )}
        onMouseEnter={(e) => {
          if (!enableHeaderHoverBg) return;
          const element = e.currentTarget;
          const computedStyle = getComputedStyle(document.documentElement);
          const mutedColor = computedStyle.getPropertyValue("--muted").trim();
          element.style.backgroundColor = mutedColor ? `hsl(${mutedColor})` : "";
        }}
        onMouseLeave={(e) => {
          if (!enableHeaderHoverBg) return;
          e.currentTarget.style.backgroundColor = "";
        }}
      >
        <button
          onClick={onToggle}
          className="flex flex-1 items-center gap-2 px-3 py-2 text-left"
        >
          {isExpanded ? (
            <ChevronDown className="w-3 h-3 text-muted-foreground flex-shrink-0" />
          ) : (
            <ChevronRight className="w-3 h-3 text-muted-foreground flex-shrink-0" />
          )}
          {icon && (
            <span className="text-muted-foreground flex-shrink-0">{icon}</span>
          )}
          <span
            className={cn(
              "text-[11px] font-bold uppercase tracking-[0.06em]",
              variant === "highlighted"
                ? "text-primary"
                : "text-muted-foreground/80"
            )}
          >
            {title}
          </span>
          {count !== undefined && (
            <span className="ml-auto rounded-full bg-muted px-1.5 py-0.5 text-[10px] text-muted-foreground">
              {count}
            </span>
          )}
        </button>
        {actions && (
          <div className="flex items-center gap-0.5 pr-2 flex-shrink-0">
            {actions}
          </div>
        )}
      </div>

      {/* Section content */}
      {isExpanded && <div className="py-1.5">{children}</div>}
    </div>
  );
}

interface SidebarSectionItemProps {
  /** Content to render */
  children: ReactNode;
  /** Click handler */
  onClick?: () => void;
  /** Whether item is selected/active */
  isActive?: boolean;
  /** Additional CSS classes */
  className?: string;
}

/**
 * Individual item within a SidebarSection.
 * Use for consistent item styling in lists.
 */
export function SidebarSectionItem({
  children,
  onClick,
  isActive,
  className,
}: SidebarSectionItemProps) {
  const Component = onClick ? "button" : "div";

  return (
    <Component
      onClick={onClick}
      className={cn(
        "w-full flex items-center gap-2 px-3 py-1.5 text-left text-sm rounded-md",
        "transition-colors",
        onClick && "cursor-pointer hover:bg-muted/50",
        isActive && "bg-primary/10 text-primary",
        className
      )}
    >
      {children}
    </Component>
  );
}