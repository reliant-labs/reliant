/**
 * Our theme, translated into the one dialect Stripe's iframes speak.
 *
 * ── Why this is read at runtime and not written as a constant ─────────
 *
 * The card fields are Stripe-hosted iframes. Nothing in our stylesheet
 * reaches inside them — not a Tailwind class, not a CSS variable, not a
 * selector — so the ONLY way to theme them is to hand Stripe concrete colour
 * values through the `appearance` API.
 *
 * Concrete is the problem. This app has a light and a dark mode (`.dark`) AND
 * several palettes on top of that (`data-color-scheme`), so any colour written
 * as a literal here is correct for exactly one combination and wrong for the
 * rest — and wrong LOUDLY, since a mismatched card field on a dark page is the
 * white box this whole change exists to remove.
 *
 * So the values are read from the live computed styles at mount. Whatever the
 * page is actually wearing is what Stripe is told to wear.
 *
 * ── What this can and cannot achieve ─────────────────────────────────
 *
 * Close, not identical. `appearance` exposes a fixed vocabulary — colours,
 * radius, font, and a limited set of per-class rules — so the field
 * backgrounds, text, borders, placeholders and focus ring can all be matched.
 * What it does not expose is our exact focus-ring geometry or transition
 * timing, and Stripe's own error text and iconography keep their shapes. The
 * honest description is "reads as part of the page", not "pixel-identical to a
 * native input".
 */

import type { Appearance } from "@stripe/stripe-js";

/**
 * Reads a CSS custom property and returns it as a colour Stripe accepts.
 *
 * The tokens are stored as bare HSL triples (`222 47% 11%`) because Tailwind
 * composes them with alpha — `hsl(var(--background) / 0.5)`. Stripe needs a
 * complete colour function, so the triple is wrapped. A token that is already
 * a complete colour (a hex, or an `hsl(...)`) is passed through untouched, so
 * this keeps working if the token format ever changes.
 */
function cssColor(name: string, fallback: string): string {
  if (typeof window === "undefined") return fallback;
  const raw = getComputedStyle(document.documentElement)
    .getPropertyValue(name)
    .trim();
  if (!raw) return fallback;
  if (raw.startsWith("#") || raw.includes("(")) return raw;
  // A bare triple: "222 47% 11%".
  return `hsl(${raw})`;
}

function cssLength(name: string, fallback: string): string {
  if (typeof window === "undefined") return fallback;
  const raw = getComputedStyle(document.documentElement)
    .getPropertyValue(name)
    .trim();
  return raw || fallback;
}

/**
 * The appearance for a payment form embedded in one of our own surfaces.
 *
 * Built against `--card` rather than `--background`, because that is what the
 * form actually sits on: both checkout surfaces are a bordered card lifted
 * above the page. Theming to the page colour instead is the subtle version of
 * the same bug — a panel that is *nearly* the right dark, which reads as a
 * seam running through the middle of the form.
 */
export function checkoutAppearance(): Appearance {
  const card = cssColor("--card", "hsl(222 47% 17%)");
  const foreground = cssColor("--foreground", "hsl(210 40% 98%)");
  const muted = cssColor("--muted-foreground", "hsl(215 20% 65%)");
  const border = cssColor("--border", "hsl(217 33% 32%)");
  const primary = cssColor("--primary", "hsl(217 91% 70%)");
  const destructive = cssColor("--destructive", "hsl(0 70% 55%)");
  // `--muted` is the input fill: a step off the card, the same relationship
  // our own inputs have to the surface they sit on.
  const inputBg = cssColor("--muted", "hsl(217 33% 21%)");
  const radius = cssLength("--radius", "0.5rem");

  return {
    // "night" over "stripe": the base theme decides everything the variables
    // below do NOT name — internal dividers, dropdown chrome, the autofill
    // treatment. Starting from the light base and overriding colours leaves
    // those details light, which is exactly the mismatch that reads as broken.
    theme: "night",
    variables: {
      colorPrimary: primary,
      colorBackground: inputBg,
      colorText: foreground,
      colorTextSecondary: muted,
      colorTextPlaceholder: muted,
      colorDanger: destructive,
      borderRadius: radius,
      // Inherited so the form uses the same face as the page around it. A
      // web font would have to be loaded into the iframe separately, which is
      // a request we do not need for a five-field form.
      fontFamily:
        'ui-sans-serif, system-ui, -apple-system, "Segoe UI", sans-serif',
      fontSizeBase: "14px",
      spacingUnit: "4px",
    },
    rules: {
      ".Input": {
        backgroundColor: inputBg,
        border: `1px solid ${border}`,
        boxShadow: "none",
        color: foreground,
      },
      ".Input:focus": {
        border: `1px solid ${primary}`,
        // Approximates our focus ring (`ring-2 ring-primary`). Stripe has no
        // outline primitive, so this is a shadow standing in for one — close
        // in weight, not the identical geometry.
        boxShadow: `0 0 0 2px ${primary}40`,
      },
      ".Input--invalid": {
        border: `1px solid ${destructive}`,
        boxShadow: "none",
        color: foreground,
      },
      ".Label": { color: muted, fontWeight: "500" },
      ".Tab": {
        backgroundColor: inputBg,
        border: `1px solid ${border}`,
        boxShadow: "none",
        color: foreground,
      },
      ".Tab:hover": { backgroundColor: card, color: foreground },
      ".Tab--selected": {
        backgroundColor: card,
        border: `1px solid ${primary}`,
        boxShadow: "none",
        color: foreground,
      },
      ".Error": { color: destructive },
    },
  };
}
