import type { ReactNode } from "react";
import { ChevronRight } from "lucide-react";
import { cn } from "../../../lib/utils";

interface ChangeSectionProps {
  title: string;
  count?: number;
  expanded: boolean;
  onToggle: () => void;
  /** Section-wide actions (stage all, discard all, …). */
  actions?: ReactNode;
  children: ReactNode;
}

/** A collapsible group inside the Changes panel, headed by a forge section
 *  label ("STAGED 3"). */
export function ChangeSection({ title, count, expanded, onToggle, actions, children }: ChangeSectionProps) {
  return (
    <section className="flex flex-col">
      <div className="flex h-7 items-center gap-1 pl-1 pr-2">
        <button
          type="button"
          onClick={onToggle}
          aria-expanded={expanded}
          className={cn(
            "flex min-w-0 flex-1 items-center gap-1 rounded-md px-1 py-0.5 text-left",
            "text-xs font-semibold uppercase tracking-wide text-muted-foreground transition-colors hover:text-foreground",
            "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50",
          )}
        >
          <ChevronRight
            className={cn("h-3.5 w-3.5 shrink-0 transition-transform", expanded && "rotate-90")}
            aria-hidden="true"
          />
          <span className="truncate">{title}</span>
          {count !== undefined && (
            <span className="font-medium tabular-nums text-muted-foreground/80">{count}</span>
          )}
        </button>
        {actions && <div className="flex items-center gap-0.5">{actions}</div>}
      </div>
      {expanded && children}
    </section>
  );
}
