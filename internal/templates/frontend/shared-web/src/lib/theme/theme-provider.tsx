import { brandThemeToCssVars, type BrandTheme } from "./brand-theme";

import type { ReactNode } from "react";

/**
 * Theme-token → CSS-var provider for white-label / per-brand theming.
 *
 * `globals.css` defines the app's semantic design tokens as CSS custom
 * properties (`--color-accent`, `--color-surface`, …) with light + dark
 * defaults. This provider layers a per-brand theme ON TOP of those defaults
 * by setting the same custom properties inline on a wrapper: every token the
 * brand specifies wins for the subtree; every token left null/undefined/empty
 * falls back to the compiled-in globals.css value (including its dark-mode
 * variant). So a partial brand — say just an accent color — recolors exactly
 * that, and the rest of the palette stays coherent.
 *
 *   // brand row fetched per brand/request:
 *   <ThemeProvider theme={{ accent: brand.primaryColor, accentHover: brand.primaryDark }}>
 *     <App />
 *   </ThemeProvider>
 *
 * For SSR (no color flash), resolve the brand server-side and apply
 * brandThemeToCssVars (./brand-theme) to <html> in the root layout instead
 * of wrapping on the client.
 */
export function ThemeProvider({
  theme,
  children,
}: {
  theme?: BrandTheme | null;
  children: ReactNode;
}) {
  const vars = brandThemeToCssVars(theme);
  // display:contents keeps this wrapper out of the layout box tree while its
  // CSS custom properties still inherit to every descendant — so a brand
  // theme recolors the whole subtree without introducing a layout node.
  //
  // The inline style is the exception react/forbid-dom-props exists to allow:
  // `vars` is computed at RUNTIME from the brand row, so it is neither a
  // Tailwind utility nor a static custom-property block. The exemption is
  // declared in the Next.js eslint.config.mjs scoped to this path, NOT as an
  // in-file directive — this file also ships into the Vite SPA tree, whose
  // config does not load eslint-plugin-react, and a directive naming an
  // unloaded rule is a hard eslint error there.
  return <div style={{ display: "contents", ...vars }}>{children}</div>;
}
