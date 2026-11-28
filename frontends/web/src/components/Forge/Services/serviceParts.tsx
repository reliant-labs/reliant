// Copyright (c) 2026 Reliant Labs

/** The pieces a service's page and a static site's page share: a section with its heading. */

import type { ReactNode } from "react";

export function Section({ title, meta, actions, testId, children }: { title: ReactNode; meta?: ReactNode; actions?: ReactNode; testId?: string; children: ReactNode }) {
  return (
    <section className="min-w-0 space-y-3 pt-4" data-testid={testId}>
      <div className="flex min-h-7 items-center justify-between gap-3">
        <h2 className="min-w-0 text-section text-ink">
          {title}
          {meta && <span className="ml-2 text-meta font-normal text-ink-muted">{meta}</span>}
        </h2>
        {actions && <div className="flex shrink-0 items-center gap-2">{actions}</div>}
      </div>
      {children}
    </section>
  );
}
