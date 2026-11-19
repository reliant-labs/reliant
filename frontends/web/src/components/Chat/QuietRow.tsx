/**
 * The transcript's quiet one-line row: a tool call, a thought, a notice.
 *
 * No card, no border box, no stripe, no tinted fill — an icon, a line of
 * `text-ui` in muted ink, and, when the row has more to show, a disclosure:
 * click (or Enter / Space) to open the detail inline beneath it, under one
 * quiet indent rule. Rows sit tight against each other; the prose around them
 * gets the space.
 *
 * The header is a `div role="button"` rather than a `<button>` because its
 * trailing slot may hold real buttons (cancel, open file), and a button may
 * not contain another.
 */

import { useId, type KeyboardEvent, type ReactNode } from "react";
import { ChevronRight } from "lucide-react";
import { cn } from "../../lib/utils";

/** The row's line, for a row that is not a disclosure (and the detail's indent). */
export const QUIET_ROW_LINE_CLASS =
  "flex min-h-7 min-w-0 items-center gap-2 rounded-md text-ui text-muted-foreground touch:min-h-[44px]";

/** The 16px leading slot every row's icon sits in, so their text aligns. */
export const QUIET_ROW_ICON_SLOT_CLASS = "inline-flex size-4 shrink-0 items-center justify-center";

/** The open detail: indented under the icon, one quiet rule, room below. */
export const QUIET_ROW_DETAIL_CLASS = "mb-3 ml-2 mt-1 border-l border-border-subtle pl-4";

interface QuietRowProps {
  /** The 16px leading icon (or the running spinner / failed mark). */
  icon: ReactNode;
  /** The line: a sans verb, then the target. Truncates as one line. */
  children: ReactNode;
  /** After the line: counts, a duration, actions. Clicks here never toggle. */
  trailing?: ReactNode;
  /** Present when the row has detail to open. */
  expanded?: boolean;
  onToggle?: () => void;
  /** The disclosure's accessible name. Defaults to the line's text. */
  ariaLabel?: string;
  /** Rendered beneath the row while expanded. */
  detail?: ReactNode;
  /** A failed row's line reads in destructive ink. */
  tone?: "default" | "destructive";
  className?: string;
  "data-testid"?: string;
  /** A test id for the open detail (the element `aria-controls` names). */
  detailTestId?: string;
  /**
   * "button": the header is a real `<button>`. Only for a row whose trailing
   * slot holds no control of its own (a button may not contain another).
   */
  as?: "button" | "div";
}

export function QuietRow({
  icon,
  children,
  trailing,
  expanded = false,
  onToggle,
  ariaLabel,
  detail,
  tone = "default",
  className,
  "data-testid": testId,
  detailTestId,
  as = "div",
}: QuietRowProps) {
  const panelId = useId();
  const expandable = Boolean(onToggle);
  const onKeyDown = (event: KeyboardEvent<HTMLDivElement>) => {
    if (!onToggle || event.target !== event.currentTarget) return;
    if (event.key === "Enter" || event.key === " ") {
      event.preventDefault();
      onToggle();
    }
  };

  const asButton = as === "button" && expandable;
  const Header = asButton ? "button" : "div";
  const headerProps = asButton
    ? { type: "button" as const }
    : {
        role: expandable ? "button" : undefined,
        tabIndex: expandable ? 0 : undefined,
        onKeyDown: expandable ? onKeyDown : undefined,
      };

  return (
    <div className={cn("group/row min-w-0", className)} data-testid={testId}>
      <Header
        {...headerProps}
        className={cn(
          QUIET_ROW_LINE_CLASS,
          "-mx-1.5 w-[calc(100%+0.75rem)] px-1.5 text-left transition-colors duration-150",
          tone === "destructive" && "text-destructive-ink",
          expandable &&
            "cursor-pointer hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring",
          expanded && "text-foreground",
        )}
        aria-expanded={expandable ? expanded : undefined}
        aria-controls={expandable && expanded ? panelId : undefined}
        aria-label={expandable ? ariaLabel : undefined}
        onClick={onToggle}
      >
        <span className={QUIET_ROW_ICON_SLOT_CLASS} aria-hidden="true">
          {icon}
        </span>
        <span className="flex min-w-0 items-baseline gap-1.5 truncate">{children}</span>
        {expandable && (
          <ChevronRight
            aria-hidden="true"
            className={cn(
              "size-3.5 shrink-0 opacity-0 transition-[opacity,transform] duration-150 group-hover/row:opacity-70 group-focus-within/row:opacity-70",
              expanded && "rotate-90 opacity-70",
            )}
          />
        )}
        {trailing && (
          // Stops the click here: an action in the trailing slot never also
          // opens the row.
          <span
            className="ml-auto flex shrink-0 items-center gap-1.5 pl-2"
            onClick={asButton ? undefined : (event) => event.stopPropagation()}
            onKeyDown={asButton ? undefined : (event) => event.stopPropagation()}
          >
            {trailing}
          </span>
        )}
      </Header>
      {expanded && detail !== undefined && (
        <div id={panelId} className={QUIET_ROW_DETAIL_CLASS} data-testid={detailTestId}>
          {detail}
        </div>
      )}
    </div>
  );
}
