/**
 * The transcript's one way to say something went wrong: a quiet inline row,
 * in the same language as a tool row (QuietRow) and a sub-agent row.
 *
 * No card, no border box, no tinted fill. The status lives in a small icon in
 * the semantic colour (destructive for a failure, warning for a policy stop,
 * a spinning warning mark while a step is retried); the message reads in
 * plain ink beside it. When there is more to say (a stack, the raw error) the
 * row is a disclosure with a chevron, and the detail opens beneath it under
 * the same indent rule a tool row's detail uses.
 *
 * Actions (Retry, Copy, Dismiss) sit after the line, outside the disclosure
 * button, so a button never holds another. They show on hover or keyboard
 * focus and are always shown on touch, which has no hover. An action that IS
 * the way out (Open Settings → Providers) goes in `persistentActions` and
 * stays visible: hiding the only fix behind a hover would be a regression.
 */

import { useId, useState, type ReactNode } from "react";
import { ChevronRight, XCircle } from "lucide-react";
import { cn } from "../../lib/utils";
import { QUIET_ROW_DETAIL_CLASS, QUIET_ROW_ICON_SLOT_CLASS } from "./QuietRow";

export type ErrorRowTone = "error" | "warning" | "quiet";

/** Revealed on hover or focus of the row; always shown on touch. */
export const ERROR_ROW_REVEAL_CLASS =
  "opacity-0 transition-opacity duration-150 group-hover/err:opacity-100 group-focus-within/err:opacity-100 touch:opacity-100";

/**
 * A row action: a 24px icon button, named by its aria-label and tooltip, the
 * same size as a tool row's actions so the row keeps one right edge.
 */
export const ERROR_ROW_ACTION_CLASS =
  "touch-target inline-flex size-6 items-center justify-center rounded-md text-muted-foreground transition-colors hover:bg-foreground/5 hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:opacity-60";

interface ErrorRowProps {
  /** The leading mark. Defaults to a destructive XCircle. */
  icon?: ReactNode;
  /**
   * How loud the line reads: "error" in foreground ink (it stopped the turn),
   * "warning" likewise, "quiet" in muted ink (a retry that may clear).
   */
  tone?: ErrorRowTone;
  /** The message. One truncated line unless `wrap`; the full text is in `details`. */
  children: ReactNode;
  /**
   * Let the message wrap instead of truncating: for a failure whose summary
   * is the instruction ("Reconnect Codex in Settings → Providers").
   */
  wrap?: boolean;
  /** After the message, inside the disclosure: an attempt count, a time. */
  meta?: ReactNode;
  /** Opened beneath the row by the chevron. Absent: the row does not expand. */
  details?: ReactNode;
  /** Revealed on hover / focus (always on touch). */
  actions?: ReactNode;
  /** Always visible: the action that fixes the error. */
  persistentActions?: ReactNode;
  /** Controlled expansion; omit both to let the row hold its own. */
  expanded?: boolean;
  onToggle?: () => void;
  /** The disclosure's accessible name. Defaults to its text. */
  ariaLabel?: string;
  /** Passed through to the root: an alert row keeps the role it had. */
  role?: string;
  "aria-live"?: "polite" | "assertive" | "off";
  className?: string;
  "data-testid"?: string;
  /** Test ids for the parts tests pin. */
  chevronTestId?: string;
  detailTestId?: string;
}

export function ErrorRow({
  icon,
  tone = "error",
  children,
  wrap = false,
  meta,
  details,
  actions,
  persistentActions,
  expanded: controlledExpanded,
  onToggle,
  ariaLabel,
  role,
  "aria-live": ariaLive,
  className,
  "data-testid": testId,
  chevronTestId,
  detailTestId,
}: ErrorRowProps) {
  const panelId = useId();
  const [ownExpanded, setOwnExpanded] = useState(false);
  const expanded = controlledExpanded ?? ownExpanded;
  const toggle = onToggle ?? (() => setOwnExpanded((v) => !v));
  const expandable = details !== undefined && details !== null && details !== false;

  const mark = icon ?? <XCircle className="size-4 text-destructive-ink" />;
  const lineInk = tone === "quiet" ? "text-muted-foreground" : "text-foreground";

  const line = (
    <>
      <span className={cn(QUIET_ROW_ICON_SLOT_CLASS, wrap && "mt-0.5")} aria-hidden="true">
        {mark}
      </span>
      <span className={cn("min-w-0 flex-1", wrap ? "whitespace-normal break-words" : "truncate")}>{children}</span>
      {meta}
      {expandable && (
        <ChevronRight
          aria-hidden="true"
          data-testid={chevronTestId}
          className={cn(
            "size-3.5 shrink-0 text-muted-foreground transition-transform duration-150",
            wrap && "mt-1",
            expanded && "rotate-90",
          )}
        />
      )}
    </>
  );

  return (
    <div className={cn("group/err min-w-0", className)} role={role} aria-live={ariaLive} data-testid={testId}>
      <div className={cn("flex min-h-7 min-w-0 gap-1.5 touch:min-h-[44px]", wrap ? "items-start" : "items-center")}>
        {expandable ? (
          <button
            type="button"
            onClick={toggle}
            aria-expanded={expanded}
            aria-controls={expanded ? panelId : undefined}
            aria-label={ariaLabel}
            className={cn(
              "-mx-1.5 flex min-h-7 min-w-0 flex-1 gap-2 rounded-md px-1.5 text-left text-ui transition-colors duration-150 hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring touch:min-h-[44px]",
              wrap ? "items-start py-1" : "items-center",
              lineInk,
            )}
          >
            {line}
          </button>
        ) : (
          <div className={cn("flex min-h-7 min-w-0 flex-1 gap-2 text-ui", wrap ? "items-start py-1" : "items-center", lineInk)}>{line}</div>
        )}
        {persistentActions && <span className="flex shrink-0 items-center gap-1">{persistentActions}</span>}
        {actions && <span className={cn("flex shrink-0 items-center gap-0.5", ERROR_ROW_REVEAL_CLASS)}>{actions}</span>}
      </div>
      {expandable && expanded && (
        <div id={panelId} className={QUIET_ROW_DETAIL_CLASS} data-testid={detailTestId}>
          {details}
        </div>
      )}
    </div>
  );
}
