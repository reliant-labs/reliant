/**
 * A tool's error result inside its open row: a small status icon and the
 * message in plain ink. No tinted fill, no box — the same calm line the
 * transcript's ErrorRow draws, sized for a tool body.
 */

import { memo, type ReactNode } from "react";
import { AlertCircle, XCircle } from "lucide-react";
import { cn } from "../../../lib/utils";

interface ToolErrorTextProps {
  children: ReactNode;
  /** "warning" for a policy stop (blocked, denied), "error" for a failure. */
  tone?: "error" | "warning";
  className?: string;
  "data-testid"?: string;
}

function ToolErrorTextComponent({ children, tone = "error", className, "data-testid": testId }: ToolErrorTextProps) {
  const Icon = tone === "warning" ? AlertCircle : XCircle;
  return (
    <div className={cn("flex min-w-0 items-start gap-2 px-2 py-1.5 text-xs text-foreground/90", className)} data-testid={testId}>
      <Icon
        aria-hidden="true"
        className={cn("mt-px size-3.5 shrink-0", tone === "warning" ? "text-warning-ink" : "text-destructive-ink")}
      />
      <span className="min-w-0 whitespace-pre-wrap break-words">{children}</span>
    </div>
  );
}

export const ToolErrorText = memo(ToolErrorTextComponent);
