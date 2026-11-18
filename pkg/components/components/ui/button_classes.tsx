/**
 * The Button's classes, apart from the component: its variants, its size
 * scale, and buttonClasses() for an element Button cannot render itself.
 *
 * Its own module, not an export of button.tsx, because a file that exports
 * a component AND a function defeats React Fast Refresh: an edit to it
 * reloads the page instead of hot-swapping the component
 * (eslint-plugin-react-refresh's only-export-components). See button.tsx
 * for the size table, the states and what className can and cannot do.
 */
export type ButtonVariant = "primary" | "secondary" | "outline" | "ghost" | "danger";
export type ButtonSize = "xs" | "sm" | "md" | "lg" | "xl";

export interface ButtonStyleOptions {
  /** Visual emphasis. Defaults to "primary". */
  variant?: ButtonVariant;
  /** Control height; see the size table above. Defaults to "md". */
  size?: ButtonSize;
  /** Stretch to the container's width. */
  fullWidth?: boolean;
  /** A square, icon-only button. Set by `icon`; pass it to `buttonClasses` for your own element. */
  iconOnly?: boolean;
}

const baseStyles = [
  "relative inline-flex shrink-0 select-none items-center justify-center whitespace-nowrap font-medium",
  "transition duration-150 ease-out motion-reduce:transition-none",
  "focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent",
  "motion-safe:not-disabled:active:translate-y-px",
  "disabled:not-aria-busy:cursor-not-allowed disabled:not-aria-busy:opacity-50 aria-busy:cursor-progress",
].join(" ");

// Neutral variants step through `ink` at low alpha rather than a surface
// token: an overlay reads as the same step on a card, the page and an inset,
// in light and dark, where a fixed fill matches one of them and vanishes on
// another. The filled variants press one step past hover with a brightness
// filter, so the step stays relative to whatever the theme's fill is.
//
// internal/templates/frontend_button_contrast_test.go reads these strings
// and checks every rest and hover pair against both scaffold palettes, so
// keep each variant a single string literal of plain utilities.
const variantStyles: Record<ButtonVariant, string> = {
  primary:
    "bg-accent text-on-accent not-disabled:hover:bg-accent-hover not-disabled:active:bg-accent-hover not-disabled:active:brightness-95",
  secondary:
    "bg-ink/6 text-ink not-disabled:hover:bg-ink/10 not-disabled:active:bg-ink/14 aria-expanded:bg-ink/14 aria-pressed:bg-ink/14",
  outline:
    "border border-border-strong text-ink not-disabled:hover:bg-ink/5 not-disabled:active:bg-ink/10 aria-expanded:bg-ink/10 aria-pressed:bg-ink/10",
  ghost:
    "text-ink-muted not-disabled:hover:bg-ink/6 not-disabled:hover:text-ink not-disabled:active:bg-ink/10 aria-expanded:bg-ink/10 aria-expanded:text-ink aria-pressed:bg-ink/10 aria-pressed:text-ink",
  danger:
    "bg-danger text-on-danger not-disabled:hover:bg-danger-hover not-disabled:active:bg-danger-hover not-disabled:active:brightness-95",
};

interface SizeStyle {
  /** Height, type, gap and radius. */
  box: string;
  /** Inline padding, and the tighter padding beside an icon (icons carry their own side bearing). */
  pad: string;
  padBesideIcon: { start: string; end: string };
  /** Width and height of an icon-only button. */
  square: string;
  /** The icon size, applied to every svg in an icon slot. */
  icon: string;
}

const sizeStyles: Record<ButtonSize, SizeStyle> = {
  xs: {
    box: "h-6 gap-1 rounded-sm text-xs",
    pad: "ps-2 pe-2",
    padBesideIcon: { start: "ps-1.5", end: "pe-1.5" },
    square: "size-6",
    icon: "[&_svg]:size-3",
  },
  sm: {
    box: "h-7 gap-1.5 rounded-md text-xs",
    pad: "ps-2.5 pe-2.5",
    padBesideIcon: { start: "ps-2", end: "pe-2" },
    square: "size-7",
    icon: "[&_svg]:size-3.5",
  },
  md: {
    box: "h-8 gap-1.5 rounded-md text-sm",
    pad: "ps-3 pe-3",
    padBesideIcon: { start: "ps-2.5", end: "pe-2.5" },
    square: "size-8",
    icon: "[&_svg]:size-4",
  },
  lg: {
    box: "h-9 gap-2 rounded-md text-sm",
    pad: "ps-4 pe-4",
    padBesideIcon: { start: "ps-3", end: "pe-3" },
    square: "size-9",
    icon: "[&_svg]:size-4",
  },
  xl: {
    box: "h-10 gap-2 rounded-md text-base",
    pad: "ps-5 pe-5",
    padBesideIcon: { start: "ps-4", end: "pe-4" },
    square: "size-10",
    icon: "[&_svg]:size-5",
  },
};

export interface PaddingOptions {
  leftIcon?: boolean;
  rightIcon?: boolean;
}

function padding(s: SizeStyle, iconOnly: boolean, icons: PaddingOptions): string {
  if (iconOnly) return `${s.square} p-0`;
  const [start, end] = s.pad.split(" ");
  return [
    icons.leftIcon ? s.padBesideIcon.start : start,
    icons.rightIcon ? s.padBesideIcon.end : end,
  ].join(" ");
}

/** Every class of a button: base, variant, size box, padding, width, then className. */
export function composeClasses(
  { variant = "primary", size = "md", fullWidth, iconOnly = false }: ButtonStyleOptions,
  icons: PaddingOptions,
  className?: string,
): string {
  const s = sizeStyles[size];
  return [
    baseStyles,
    variantStyles[variant],
    s.box,
    padding(s, iconOnly, icons),
    fullWidth ? "w-full" : "",
    className ?? "",
  ]
    .filter(Boolean)
    .join(" ");
}

/**
 * The button's classes, for an element Button cannot render itself — a
 * <label> that opens a file input, a router link with props of its own.
 * The element then needs its own focus handling and accessible name.
 */
export function buttonClasses(options: ButtonStyleOptions = {}, className?: string): string {
  return composeClasses(options, {}, className);
}

/** The icon size for a button size, applied to every svg in an icon slot. */
export function buttonIconSize(size: ButtonSize): string {
  return sizeStyles[size].icon;
}
