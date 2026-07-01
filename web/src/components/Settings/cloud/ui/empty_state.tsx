import type { LucideIcon } from "lucide-react";
import type { ReactNode } from "react";
import { cn } from "../../../../lib/utils";

/**
 * EmptyState — centered "no data yet" panel with an icon, title, description
 * and optional CTA. Ported from admin-web's `ui/empty_state.tsx`; the dashed
 * frame and muted icon chip use reliant tokens (`border-border`,
 * `bg-muted`, `text-muted-foreground`).
 */
export interface EmptyStateProps {
  icon: LucideIcon;
  title: string;
  description: string;
  action?: ReactNode;
  className?: string;
}

export function EmptyState({
  icon: Icon,
  title,
  description,
  action,
  className,
}: EmptyStateProps) {
  return (
    <div
      className={cn(
        "flex flex-col items-center justify-center rounded-lg border border-dashed border-border bg-card px-6 py-16 text-center",
        className
      )}
    >
      <div className="flex h-12 w-12 items-center justify-center rounded-lg bg-muted">
        <Icon className="h-6 w-6 text-muted-foreground" />
      </div>
      <h3 className="mt-4 text-sm font-semibold text-foreground">{title}</h3>
      <p className="mt-1 text-sm text-muted-foreground">{description}</p>
      {action && <div className="mt-4">{action}</div>}
    </div>
  );
}

export default EmptyState;
