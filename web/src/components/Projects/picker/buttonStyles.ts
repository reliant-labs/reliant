import { cn } from "@/lib/utils";

/**
 * Button class strings for the project picker, matching forge's PageHeader
 * action styles (`components/forge-ui/page_header.tsx`).
 *
 * Why not `ui/Button`: the picker renders inside `.forge-ui`, where `accent`
 * is re-pointed at `--primary`. `ui/Button`'s outline and ghost variants hover
 * to `bg-accent`, so inside this wrapper they flood to a solid primary fill on
 * hover. Forge's own vocabulary (accent fill for the one primary action, a
 * bordered surface for secondaries) is what the rest of the page speaks.
 *
 * `disabled:pointer-events-none` is deliberate: a disabled button swallows
 * no hover events, so the Tooltip wrapper around it still receives them and
 * can explain WHY the action is unavailable.
 */
export type PickerButtonVariant = "primary" | "secondary" | "ghost" | "danger";
export type PickerButtonSize = "sm" | "md";

const VARIANTS: Record<PickerButtonVariant, string> = {
  primary: "bg-accent text-on-accent shadow-sm hover:bg-accent-hover",
  secondary: "border border-border-strong bg-surface text-ink shadow-sm hover:bg-surface-muted",
  ghost: "text-ink-muted hover:bg-muted hover:text-ink",
  danger: "border border-danger-border bg-surface text-danger hover:bg-danger-surface",
};

const SIZES: Record<PickerButtonSize, string> = {
  sm: "h-7 gap-1.5 px-2.5 text-xs",
  md: "h-8 gap-1.5 px-3 text-sm",
};

export function pickerButton(
  variant: PickerButtonVariant = "secondary",
  size: PickerButtonSize = "md",
  className?: string,
): string {
  return cn(
    "inline-flex shrink-0 items-center justify-center whitespace-nowrap rounded-md font-medium transition-colors",
    "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/60 focus-visible:ring-offset-2 focus-visible:ring-offset-background",
    "disabled:pointer-events-none disabled:opacity-50",
    VARIANTS[variant],
    SIZES[size],
    className,
  );
}

/** A square icon-only button. Always pair with a Tooltip and an aria-label. */
export function pickerIconButton(tone: "neutral" | "danger" = "neutral", className?: string): string {
  return cn(
    "inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-md text-ink-muted transition-colors",
    "focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent/60",
    tone === "danger"
      ? "hover:bg-danger-surface hover:text-danger"
      : "hover:bg-muted hover:text-ink",
    className,
  );
}
