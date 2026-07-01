import React from "react";
import { cn } from "../../../../lib/utils";

/**
 * PageHeader — title / subtitle / actions header for a cloud settings
 * section. Ported from admin-web's `ui/page_header.tsx` but stripped of the
 * Next.js `next/link` dependency (reliant is a Vite SPA); pass an `actions`
 * node for the right-aligned action cluster. Colors are reliant tokens.
 */
export interface PageHeaderProps {
  title: React.ReactNode;
  subtitle?: React.ReactNode;
  actions?: React.ReactNode;
  className?: string;
}

export function PageHeader({
  title,
  subtitle,
  actions,
  className,
}: PageHeaderProps) {
  return (
    <div
      className={cn(
        "mb-6 flex items-start justify-between gap-4 border-b border-border pb-4",
        className
      )}
    >
      <div>
        <h1 className="text-2xl font-semibold tracking-tight text-foreground">
          {title}
        </h1>
        {subtitle && (
          <p className="mt-1 text-sm text-muted-foreground">{subtitle}</p>
        )}
      </div>
      {actions && <div className="flex items-center gap-2">{actions}</div>}
    </div>
  );
}

export default PageHeader;
