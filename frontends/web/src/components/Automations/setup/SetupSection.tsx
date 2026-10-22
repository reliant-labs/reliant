// Copyright (c) 2025 Reliant Labs

/**
 * One numbered section of the setup dialog: "(1) GitHub", "(2) What it
 * watches". A fieldset, so its legend names the group for assistive tech; the
 * number is decoration and hidden from it.
 */

import type { ReactNode } from "react";

export interface SetupSectionProps {
  number?: number;
  title: string;
  /** A short line beside the title: "scoped by Repositories". */
  aside?: ReactNode;
  /** A sentence under the title. */
  description?: ReactNode;
  children: ReactNode;
  testId?: string;
}

export function SetupSection({ number, title, aside, description, children, testId }: SetupSectionProps) {
  return (
    <fieldset className="min-w-0 space-y-3" data-testid={testId}>
      <legend className="flex w-full items-baseline gap-2 text-sm font-semibold text-foreground">
        {number !== undefined && (
          <span
            aria-hidden="true"
            className="inline-flex h-5 w-5 shrink-0 items-center justify-center self-center rounded-full border border-border bg-background text-xs font-semibold text-muted-foreground"
          >
            {number}
          </span>
        )}
        <span>{title}</span>
        {aside && <span className="ml-auto text-xs font-normal text-muted-foreground">{aside}</span>}
      </legend>
      {description && <p className="-mt-1 text-xs text-muted-foreground">{description}</p>}
      <div className="space-y-4">{children}</div>
    </fieldset>
  );
}
