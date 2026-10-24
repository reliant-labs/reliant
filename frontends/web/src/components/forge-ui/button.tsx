import React from "react";

import Link from "./link";

/**
 * Button — the action primitive every other component and page composes.
 *
 * Variants, in order of emphasis:
 *
 *   primary    the one action a view exists for (accent fill)
 *   secondary  a neutral action beside it (tonal fill)
 *   outline    a neutral action that needs an edge: toolbars, card footers
 *   ghost      the quietest — row actions, icon toolbars, "Cancel"
 *   danger     an action that destroys something (danger fill)
 *
 * One primary per view. Two primaries side by side is a hierarchy problem,
 * not a styling one.
 *
 * ── Sizes ─────────────────────────────────────────────────────────────
 *
 *   size  height  text  icon   use
 *   xs    24px    12px  12px   inline in a table row or a dense toolbar
 *   sm    28px    12px  14px   toolbars, filter bars, card headers
 *   md    32px    14px  16px   the default
 *   lg    36px    14px  16px   page-header actions, dialog footers
 *   xl    40px    16px  20px   a form's submit; matches Input `md`
 *
 * The scale is the dense-console one (Stripe's dashboard, Linear, GitHub
 * Primer), not the marketing one: a 40px default makes every toolbar shout.
 * Every size clears WCAG 2.2's 24×24px target minimum (2.5.8).
 *
 * The icon size belongs to the BUTTON, not the icon: `leftIcon`, `rightIcon`
 * and `icon` are sized from the button's size, overriding a size the icon
 * carries itself, so a toolbar of mixed icons lines up. Pick the button
 * size, not the icon size.
 *
 * ── States ────────────────────────────────────────────────────────────
 *
 *   hover     one tonal step (neutral variants overlay `ink` at low alpha,
 *             so the step reads the same on a card, the page or an inset)
 *   pressed   a deeper step plus a 1px settle (none under reduced motion);
 *             `aria-expanded` / `aria-pressed` hold the pressed look for a
 *             menu trigger or a toggle
 *   focus     a 2px accent OUTLINE 2px off the edge, keyboard only
 *             (`:focus-visible`). An outline, not a box-shadow ring: Windows
 *             forced-colors mode drops box-shadows, so a shadow ring leaves
 *             a high-contrast user with no focus indicator at all. Meets
 *             WCAG 2.2 focus appearance (2.4.13): 2px thick around the whole
 *             control, and accent clears 3:1 against every page surface
 *             (asserted in internal/templates/frontend_palette_contract_test.go)
 *   disabled  50% opacity, no hover, `not-allowed` cursor
 *   loading   see below
 *
 * ── Loading ───────────────────────────────────────────────────────────
 *
 * `loading` disables the button, sets `aria-busy`, and shows a spinner
 * WITHOUT changing the button's width — a button that resizes when clicked
 * moves the thing the pointer is on. With a `leftIcon` the spinner takes
 * the icon's slot and the label stays readable ("Saving" keeps its
 * context); without one, the label is faded out in place (it keeps its
 * width and stays the accessible name) and the spinner is centred over it.
 * The busy button keeps full color: loading is not disabled-looking.
 *
 * ── Icon-only ─────────────────────────────────────────────────────────
 *
 * Pass `icon` instead of `children` for a square icon button. `aria-label`
 * is then REQUIRED by the type — an icon-only button with no name is
 * announced as just "button".
 *
 *   <Button variant="ghost" size="sm" icon={<XIcon />} aria-label="Close" />
 *
 * ── Links ─────────────────────────────────────────────────────────────
 *
 * Pass `href` and the button renders through the `Link` primitive (client
 * routing and basePath in a scaffolded app; a plain anchor for external
 * URLs). A disabled or loading link renders a disabled <button>, since a
 * link cannot be disabled. For any other element — a <label> for a file
 * input, a router link with its own props — style it with
 * `buttonClasses({ variant, size })`.
 *
 * ── className ─────────────────────────────────────────────────────────
 *
 * `className` is appended, so it EXTENDS the button (`mt-6`, `w-48`,
 * `rounded-e-none` in a split button). It cannot reliably REPLACE a property
 * the button sets — height, padding, gap, justify, a variant's colors —
 * because Tailwind orders utilities by property, not by their order in the
 * class string, and this file carries no class-merging dependency. Reach for
 * the props first (`size`, `icon` for a square, `variant`); for a deliberate
 * one-off, Tailwind's important modifier states the intent: `!justify-start`.
 *
 * Every color is a theme token, so light and dark (and a downstream
 * project's own palette) come from the theme, never from this file.
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

interface ButtonOwnProps {
  variant?: ButtonVariant;
  size?: ButtonSize;
  fullWidth?: boolean;
  /** Show a spinner, disable the button and set aria-busy. The width does not change. */
  loading?: boolean;
}

/** A labelled button: text, optionally flanked by icons. */
interface LabelledContent {
  children: React.ReactNode;
  leftIcon?: React.ReactNode;
  rightIcon?: React.ReactNode;
  icon?: never;
}

/** An icon-only button. Its accessible name is required. */
interface IconOnlyContent {
  icon: React.ReactNode;
  "aria-label": string;
  children?: never;
  leftIcon?: never;
  rightIcon?: never;
}

type NativeButtonProps = Omit<React.ButtonHTMLAttributes<HTMLButtonElement>, "children"> & {
  href?: never;
};

type LinkButtonProps = Omit<React.AnchorHTMLAttributes<HTMLAnchorElement>, "children" | "href"> & {
  /** Render as a link to this URL, through the Link primitive. */
  href: string;
  /** A disabled link renders as a disabled button. */
  disabled?: boolean;
};

export type ButtonProps = ButtonOwnProps &
  (LabelledContent | IconOnlyContent) &
  (NativeButtonProps | LinkButtonProps);

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

interface PaddingOptions {
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

function composeClasses(
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

/** A two-stroke spinner: a faint track and a quarter arc in the label color. */
function Spinner() {
  return (
    // Reduced motion: a spinner conveys "busy", so it is slowed rather than
    // frozen (WCAG 2.3.3); decorative motion elsewhere is disabled outright.
    <svg
      aria-hidden="true"
      viewBox="0 0 24 24"
      fill="none"
      className="animate-spin motion-reduce:[animation-duration:3s]"
    >
      <circle cx="12" cy="12" r="9" stroke="currentColor" strokeOpacity="0.25" strokeWidth="3" />
      <path d="M21 12a9 9 0 0 0-9-9" stroke="currentColor" strokeWidth="3" strokeLinecap="round" />
    </svg>
  );
}

const Button = React.forwardRef<HTMLButtonElement, ButtonProps>(function Button(props, ref) {
  const {
    variant = "primary",
    size = "md",
    fullWidth,
    loading = false,
    leftIcon,
    rightIcon,
    icon,
    children,
    className,
    disabled,
    ...rest
  } = props;

  const iconOnly = icon !== undefined && icon !== null;
  const iconSize = sizeStyles[size].icon;
  const inert = Boolean(disabled) || loading;
  const hasLeft = !iconOnly && leftIcon !== undefined && leftIcon !== null;
  const hasRight = !iconOnly && rightIcon !== undefined && rightIcon !== null;
  // The spinner replaces the leading icon when there is one; otherwise it is
  // centred over the content, which fades out in place to hold the width.
  const spinnerInSlot = loading && hasLeft;
  const spinnerOverlay = loading && !hasLeft;
  const fade = spinnerOverlay ? "opacity-0" : "";

  const classes = composeClasses(
    { variant, size, fullWidth, iconOnly },
    { leftIcon: hasLeft, rightIcon: hasRight },
    className,
  );

  const content = (
    <>
      {hasLeft ? (
        <span aria-hidden="true" className={`inline-flex shrink-0 ${iconSize}`}>
          {spinnerInSlot ? <Spinner /> : leftIcon}
        </span>
      ) : null}
      {iconOnly ? (
        <span aria-hidden="true" className={`inline-flex shrink-0 ${iconSize} ${fade}`}>
          {icon}
        </span>
      ) : (
        <span className={`min-w-0 truncate ${fade}`}>{children}</span>
      )}
      {hasRight ? (
        <span aria-hidden="true" className={`inline-flex shrink-0 ${iconSize} ${fade}`}>
          {rightIcon}
        </span>
      ) : null}
      {spinnerOverlay ? (
        <span
          aria-hidden="true"
          className={`absolute inset-0 inline-flex items-center justify-center ${iconSize}`}
        >
          <Spinner />
        </span>
      ) : null}
    </>
  );

  const dataAttrs = { "data-slot": "button", "data-variant": variant, "data-size": size };

  if (rest.href !== undefined) {
    const { href, ...anchorRest } = rest;
    if (inert) {
      // A link cannot be disabled; a disabled button is the honest element.
      return (
        <button
          ref={ref}
          type="button"
          disabled
          aria-busy={loading || undefined}
          aria-label={anchorRest["aria-label"]}
          id={anchorRest.id}
          title={anchorRest.title}
          className={classes}
          {...dataAttrs}
        >
          {content}
        </button>
      );
    }
    return (
      <Link href={href} className={classes} {...dataAttrs} {...anchorRest}>
        {content}
      </Link>
    );
  }

  const { type, ...buttonRest } = rest;
  return (
    <button
      ref={ref}
      type={type ?? "button"}
      disabled={inert}
      aria-busy={loading || undefined}
      className={classes}
      {...dataAttrs}
      {...buttonRest}
    >
      {content}
    </button>
  );
});

export default Button;
