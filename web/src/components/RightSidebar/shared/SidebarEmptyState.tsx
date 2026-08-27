import type { LucideIcon } from "lucide-react";
import { cn } from "../../../lib/utils";

interface SidebarEmptyStateProps {
  /** Icon to display */
  icon: LucideIcon;
  /** Primary message */
  title: string;
  /** Optional secondary description */
  description?: string;
  /** Optional action button */
  action?: {
    label: string;
    onClick: () => void;
  };
  /** Additional CSS classes */
  className?: string;
  /** Size variant */
  size?: "sm" | "md" | "lg";
}

/**
 * Empty state component for sidebar tabs.
 * Displays when a tab has no content to show.
 */
export function SidebarEmptyState({
  icon: Icon,
  title,
  description,
  action,
  className,
  size = "md",
}: SidebarEmptyStateProps) {
  const sizeClasses = {
    sm: {
      container: "py-4 px-3",
      icon: "w-5 h-5 mb-2",
      title: "text-xs",
      description: "text-2xs mt-0.5",
    },
    md: {
      container: "py-6 px-4",
      icon: "w-7 h-7 mb-3",
      title: "text-sm",
      description: "text-xs mt-1",
    },
    lg: {
      container: "py-12 px-4",
      icon: "w-12 h-12 mb-4",
      title: "text-base",
      description: "text-sm mt-2",
    },
  };

  const styles = sizeClasses[size];

  return (
    <div
      className={cn(
        "m-2 flex flex-col items-center justify-center rounded-[10px] border border-border bg-card/95 text-center shadow-sm",
        styles.container,
        className
      )}
    >
      <Icon
        className={cn("text-primary/60", styles.icon)}
      />
      <p className={cn("text-foreground font-semibold", styles.title)}>
        {title}
      </p>
      {description && (
        <p className={cn("text-muted-foreground/75", styles.description)}>
          {description}
        </p>
      )}
      {action && (
        <button
          onClick={action.onClick}
          className="mt-3 px-3 py-1.5 text-xs font-medium rounded-md bg-primary/10 text-primary hover:bg-primary/20 transition-colors"
        >
          {action.label}
        </button>
      )}
    </div>
  );
}