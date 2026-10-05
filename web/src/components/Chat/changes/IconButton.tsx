import type { MouseEvent, ReactNode } from "react";
import { Tooltip } from "../../ui/Tooltip";
import { cn } from "../../../lib/utils";

interface IconButtonProps {
  /** Accessible name. Also the tooltip unless `tooltip` is given. */
  label: string;
  tooltip?: string;
  onClick: (e: MouseEvent<HTMLButtonElement>) => void;
  disabled?: boolean;
  /** "danger" tints the hover state for destructive actions. */
  tone?: "default" | "danger";
  className?: string;
  children: ReactNode;
}

/** The one icon-only button shape the Changes panel uses: 24px, quiet until
 *  hovered, always labelled and tooltipped. */
export function IconButton({
  label,
  tooltip,
  onClick,
  disabled,
  tone = "default",
  className,
  children,
}: IconButtonProps) {
  return (
    <Tooltip content={tooltip ?? label} delay={300} wrapperClassName="flex">
      <button
        type="button"
        aria-label={label}
        onClick={onClick}
        disabled={disabled}
        className={cn(
          "inline-flex h-6 w-6 shrink-0 items-center justify-center rounded-md text-muted-foreground transition-colors",
          "hover:bg-muted hover:text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring/50",
          "disabled:pointer-events-none disabled:opacity-40",
          tone === "danger" && "hover:text-destructive",
          className,
        )}
      >
        {children}
      </button>
    </Tooltip>
  );
}
