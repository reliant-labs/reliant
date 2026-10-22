// Copyright (c) 2025 Reliant Labs

/**
 * What stopped a form from submitting, at the top of the form, as links to
 * the fields (the GOV.UK error-summary pattern). It takes focus when it
 * appears so a screen reader announces it and a keyboard user starts from it;
 * each link moves focus to its field and scrolls it into view.
 *
 * The form's primary button stays enabled: a disabled button can't be
 * focused and doesn't say why. Pressing it is what shows this.
 */

import { useEffect, useId, useRef } from "react";
import { AlertTriangle } from "lucide-react";

export interface ErrorSummaryItem {
  /** The DOM id of the field to focus. Unset for a problem no field owns. */
  fieldId?: string;
  message: string;
}

export interface ErrorSummaryProps {
  title: string;
  items: readonly ErrorSummaryItem[];
  /** Take focus on mount (and whenever `focusKey` changes). Default true. */
  autoFocus?: boolean;
  /** Changing it refocuses the summary: one value per submit attempt. */
  focusKey?: unknown;
  children?: React.ReactNode;
}

export function focusField(fieldId: string): void {
  const element = document.getElementById(fieldId);
  if (!element) return;
  element.scrollIntoView?.({ block: "center" });
  element.focus();
}

export function ErrorSummary({ title, items, autoFocus = true, focusKey, children }: ErrorSummaryProps) {
  const ref = useRef<HTMLDivElement>(null);
  const titleId = useId();
  useEffect(() => {
    if (autoFocus) ref.current?.focus();
  }, [autoFocus, focusKey]);

  return (
    <div
      ref={ref}
      role="alert"
      tabIndex={-1}
      aria-labelledby={titleId}
      className="rounded-md border border-danger-border bg-danger-surface px-3 py-3 text-sm text-danger-ink focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
      data-testid="error-summary"
    >
      <p id={titleId} className="flex items-start gap-2 font-medium">
        <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0" aria-hidden />
        {title}
      </p>
      {items.length > 0 && (
        <ul className="mt-2 space-y-1 pl-6">
          {items.map((item, index) => (
            <li key={`${item.fieldId ?? "form"}-${index}`} className="list-disc">
              {item.fieldId ? (
                <a
                  href={`#${item.fieldId}`}
                  className="underline underline-offset-2 hover:no-underline focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
                  onClick={(event) => {
                    event.preventDefault();
                    focusField(item.fieldId!);
                  }}
                >
                  {item.message}
                </a>
              ) : (
                item.message
              )}
            </li>
          ))}
        </ul>
      )}
      {children}
    </div>
  );
}
