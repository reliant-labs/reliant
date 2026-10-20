// Copyright (c) 2025 Reliant Labs

/**
 * The page header of the dashboards pages: forge-ui PageHeader's typography
 * and layout exactly (title ladder, one meta line, actions on the right),
 * with the actions as nodes — a dashboard's actions include menus (History,
 * New dashboard, More), which PageHeader's button list cannot express.
 */

import type { ReactNode } from "react";

export function DashboardHeader({
  title,
  titleAccessory,
  meta,
  actions,
  testId,
}: {
  title: ReactNode;
  titleAccessory?: ReactNode;
  meta?: ReactNode;
  actions?: ReactNode;
  testId?: string;
}) {
  return (
    <div className="flex flex-wrap items-start justify-between gap-4" data-testid={testId}>
      <div className="min-w-0">
        <div className="flex flex-wrap items-center gap-2">
          <h1 className="min-w-0 truncate text-xl font-semibold tracking-tight text-ink">{title}</h1>
          {titleAccessory}
        </div>
        {meta ? <div className="mt-1.5 flex flex-wrap items-center gap-x-4 gap-y-1 text-sm text-ink-muted">{meta}</div> : null}
      </div>
      {actions ? <div className="flex shrink-0 flex-wrap items-center gap-2">{actions}</div> : null}
    </div>
  );
}
