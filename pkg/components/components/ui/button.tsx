import React from "react";

import {
  buttonIconSize,
  composeClasses,
  type ButtonSize,
  type ButtonVariant,
} from "./button_classes";
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
 * `buttonClasses({ variant, size })` from ./button_classes.
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
export type { ButtonSize, ButtonStyleOptions, ButtonVariant } from "./button_classes";

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
  const iconSize = buttonIconSize(size);
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
