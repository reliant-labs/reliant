import type { Plugin } from "vite";

/**
 * Preloads the Latin subset of Geist Variable, the app's default UI face.
 *
 * The font's @font-face rules come from @fontsource-variable/geist (imported
 * in src/main.tsx), so the browser only discovers the file after it has
 * parsed the CSS, and the first paint uses 'Geist Fallback' (metric-matched
 * Arial, index.css). The preload starts the download with the HTML instead.
 * The file name is content-hashed, so it is read from the bundle rather than
 * written into index.html by hand; the site does the same
 * (landing-page vite.config.ts, preloadGeistLatin).
 *
 * Build-only: in dev the font is served unhashed from node_modules and there
 * is no bundle to read. Only the Latin sans file is preloaded: every other
 * subset is fetched on demand by its unicode-range, and Geist Mono paints
 * code, which is never the first thing on screen.
 */
export const GEIST_LATIN = /(?<!mono-)geist-latin-wght-normal-[\w-]+\.woff2$/;

export function preloadGeistLatin(): Plugin {
  return {
    name: "reliant:preload-geist-latin",
    apply: "build",
    transformIndexHtml: {
      order: "post",
      handler(html, ctx) {
        const font = Object.keys(ctx.bundle ?? {}).find((name) => GEIST_LATIN.test(name));
        if (!font) return html;
        const tag = `<link rel="preload" href="/${font}" as="font" type="font/woff2" crossorigin />`;
        return html.replace("</head>", `    ${tag}\n  </head>`);
      },
    },
  };
}
