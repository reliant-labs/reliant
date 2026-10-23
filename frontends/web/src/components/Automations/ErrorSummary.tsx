// Copyright (c) 2025 Reliant Labs

/**
 * What stopped a form from submitting, at the top of the form, as links to
 * the fields (the GOV.UK error-summary pattern). It takes focus when it
 * appears so a screen reader announces it and a keyboard user starts from it;
 * each link moves focus to its field and scrolls it into view.
 *
 * The form's primary button stays enabled: a disabled button can't be
 * focused and doesn't say why. Pressing it is what shows this.
 *
 * Built on forge's AlertBanner (error variant): the banner is the alert, and
 * it is named by its own title, so focusing it announces the title once.
 */

import { useEffect, useId, useRef } from "react";

import AlertBanner from "../forge-ui/alert_banner";

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
    <AlertBanner
      ref={ref}
      variant="error"
      tabIndex={-1}
      aria-labelledby={titleId}
      data-testid="error-summary"
      className="focus:outline-none focus-visible:ring-2 focus-visible:ring-ring/40"
      title={<span id={titleId}>{title}</span>}
      message={
        <>
          {items.length > 0 && (
            <ul className="space-y-1 pl-5">
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
        </>
      }
    />
  );
}
