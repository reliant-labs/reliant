// Copyright (c) 2025 Reliant Labs

/**
 * forge's semantic color vocabulary: every `--color-*` token its component
 * library is written against. The @theme bridge in src/index.css maps each
 * one onto reliant's own theme variables.
 *
 * Copied from forge's scaffolded theme (`internal/templates/frontend/vite-spa/
 * src/index.css`, identical to the Next.js `globals.css`) at the pinned forge.
 * forge publishes no machine-readable copy, so this list is the one place
 * reliant states it. Two tests keep it honest from both ends:
 *
 *   - forgeTokenBridge.test.tsx fails when an installed forge-ui component
 *     references a token the bridge does not declare, which is how a forge
 *     upgrade that adds a token shows up first — add it here then.
 *   - colorSchemeTokenContract.test.ts resolves every token below, through
 *     the bridge, to a concrete HSL color in each of the ten schemes, light and
 *     dark, inside and outside `.forge-ui`. e2e/color-schemes.spec.ts checks
 *     the same thing in a real browser.
 */
export const FORGE_COLOR_TOKENS = [
  "surface",
  "surface-muted",
  "surface-sunken",
  "border",
  "border-strong",
  "ink",
  "ink-muted",
  "ink-subtle",
  "accent",
  "accent-hover",
  "on-accent",
  "accent-surface",
  "accent-border",
  "accent-ink",
  "danger",
  "danger-hover",
  "on-danger",
  "danger-surface",
  "danger-border",
  "danger-ink",
  "success",
  "success-surface",
  "success-border",
  "success-ink",
  "warning",
  // The newest: confirmation_dialog referenced it before forge's themes
  // declared it (forge F9-7, fixed upstream in forge#1178).
  "warning-hover",
  "on-warning",
  "warning-surface",
  "warning-border",
  "warning-ink",
] as const;

export type ForgeColorToken = (typeof FORGE_COLOR_TOKENS)[number];

/** The ten values `data-color-scheme` takes (themes/professional-themes.css). */
export const COLOR_SCHEMES = [
  "professional-blue",
  "refined-neutral",
  "modern-teal",
  "slate",
  "forest",
  "vibrant-pink",
  "energetic-orange",
  "bold-red",
  "purple-classic",
  "pure-black",
] as const;
