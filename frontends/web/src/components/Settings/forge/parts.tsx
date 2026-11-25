// Small pieces the forge account sections share.

import type { ReactNode } from "react";
import { AlertCircle, CheckCircle } from "lucide-react";
import { AccountError } from "@/lib/auth/account";

/** What to tell the person about a refused account request. The broker's messages are written for them already. */
export function accountErrorMessage(err: unknown, fallback = "Something went wrong. Please try again."): string {
  if (err instanceof AccountError) {
    if (err.code === "cross_site") return "That request was blocked. Reload the page and try again.";
    return err.message || fallback;
  }
  return err instanceof Error && err.message ? err.message : fallback;
}

/** A refusal, as a status icon and text (never a tinted fill). */
export function InlineError({ message }: { message: string | null }) {
  if (!message) return null;
  return (
    <p role="alert" className="flex items-start gap-1.5 text-ui text-destructive-ink">
      <AlertCircle className="mt-0.5 h-3.5 w-3.5 shrink-0" aria-hidden="true" />
      <span>{message}</span>
    </p>
  );
}

/** A confirmation, as a status icon and text. */
export function InlineSuccess({ message }: { message: string | null }) {
  if (!message) return null;
  return (
    <p role="status" className="flex items-start gap-1.5 text-ui text-success-ink">
      <CheckCircle className="mt-0.5 h-3.5 w-3.5 shrink-0" aria-hidden="true" />
      <span>{message}</span>
    </p>
  );
}

/** One panel's heading: a panel heading and what the panel is for. */
export function SectionHeading({ title, description, action }: { title: string; description?: ReactNode; action?: ReactNode }) {
  return (
    <div className="flex items-start justify-between gap-4">
      <div className="min-w-0">
        <h3 className="text-ui font-semibold text-foreground">{title}</h3>
        {description && <p className="mt-0.5 text-meta text-muted-foreground">{description}</p>}
      </div>
      {action && <div className="shrink-0">{action}</div>}
    </div>
  );
}

/** A section of the account card, separated from the one above by a hairline. */
export function AccountSection({ children, testId }: { children: ReactNode; testId?: string }) {
  return (
    <section data-testid={testId} className="space-y-3 border-t border-border-subtle pt-4 first:border-t-0 first:pt-0">
      {children}
    </section>
  );
}
