import type { CSSProperties } from "react";

// A per-brand theme and its projection onto the semantic CSS custom
// properties globals.css declares. <ThemeProvider> (./theme-provider.tsx)
// applies it to a subtree; brandThemeToCssVars applies it anywhere else, such
// as <html> in a server-rendered root layout.
//
// It lives apart from the provider so that file exports only a component:
// React Fast Refresh can hot-swap a module only when every export is one
// (eslint-plugin-react-refresh's only-export-components).

/**
 * A brand's overrides for the app's semantic design tokens. Every field is
 * optional: a token left null/undefined/empty falls back to the compiled-in
 * globals.css value, including its dark-mode variant.
 */
export interface BrandTheme {
  surface?: string | null;
  surfaceMuted?: string | null;
  border?: string | null;
  borderStrong?: string | null;
  ink?: string | null;
  inkMuted?: string | null;
  inkSubtle?: string | null;
  accent?: string | null;
  accentHover?: string | null;
  onAccent?: string | null;
  accentSurface?: string | null;
  danger?: string | null;
  success?: string | null;
  warning?: string | null;
  /** Font stack for `--font-sans` (e.g. a brand font). */
  fontSans?: string | null;
}

// Maps each BrandTheme field to the globals.css custom property it overrides.
const TOKEN_TO_VAR: Record<keyof BrandTheme, string> = {
  surface: "--color-surface",
  surfaceMuted: "--color-surface-muted",
  border: "--color-border",
  borderStrong: "--color-border-strong",
  ink: "--color-ink",
  inkMuted: "--color-ink-muted",
  inkSubtle: "--color-ink-subtle",
  accent: "--color-accent",
  accentHover: "--color-accent-hover",
  onAccent: "--color-on-accent",
  accentSurface: "--color-accent-surface",
  danger: "--color-danger",
  success: "--color-success",
  warning: "--color-warning",
  fontSans: "--font-sans",
};

/**
 * brandThemeToCssVars projects a (partial, possibly-null) brand into a style
 * object of CSS custom properties. Null / undefined / empty values are
 * omitted entirely — the token then resolves to its globals.css default. Safe
 * to spread onto any element's `style` (a wrapper, or <html> server-side).
 */
export function brandThemeToCssVars(theme: BrandTheme | null | undefined): CSSProperties {
  const vars: Record<string, string> = {};
  if (!theme) return vars as CSSProperties;
  for (const key of Object.keys(TOKEN_TO_VAR) as (keyof BrandTheme)[]) {
    const value = theme[key];
    if (value != null && value !== "") {
      vars[TOKEN_TO_VAR[key]] = value;
    }
  }
  return vars as CSSProperties;
}
