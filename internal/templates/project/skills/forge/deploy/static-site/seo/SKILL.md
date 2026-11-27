---
name: deploy/static-site/seo
description: SEO and accessibility defaults of a new static site — head tags in the served HTML, SITE_URL as a build-time forge.FrontendURL (canonical, og:url, og:image, JSON-LD), Next.js per-page canonicals and generated robots.txt/sitemap.xml, the placeholder favicon and og.png, the shared jsx-a11y lint base, and why the Vite starter has no 404.html.
---

# SEO and accessibility for a static site

A new site starts with what search engines, link previews and assistive
technology read, so its first audit scores well before you write a line:

- **Head tags in the served HTML**, because crawlers and link previews read
  the HTML before any JavaScript runs: a title, a meta description (a real
  sentence that says to replace it), `theme-color`, the Open Graph tags
  (`og:title`, `og:description`, `og:type`, `og:image`), a large Twitter
  card, and `<link rel="icon" href="/favicon.svg">`. Vite: `index.html`.
  Next.js: the `metadata` in `src/app/layout.tsx`, with the values in
  `src/lib/site.ts`.
- **The canonical link, `og:url`, `og:image` and a `WebSite` JSON-LD block**
  need the site's absolute origin, which a static file cannot know. Each
  env's `main.k` declares it as a build-time variable:

  ```kcl
  env_vars = [forge.EnvVar {name = "SITE_URL", frontend_url = forge.FrontendURL {frontend = "web"}}]
  ```

  In dev it is the dev server's URL. When forge builds a release for a hosted
  env it is that env's live hostname: its first live custom domain, else the
  platform hostname. A release is built once and promoted by digest, so
  every env serves the origin of the env it was built for: build the release
  in prod (`forge env build prod --release …`) and staging's copy points
  search engines at prod. The first release, built before the env has a
  hostname, has an empty `SITE_URL`: forge says so, the starter leaves those
  tags out rather than inventing an origin, and the next release carries
  them. On your own bucket or Firebase, forge does not know the hostname: set
  `value = "https://example.com"` instead (a `FrontendURL` there is refused).
- **Next.js** writes the canonical per page: `export const metadata =
  pageMetadata("/pricing")` (`src/lib/site.ts`); one in the layout would name
  the home page as every page's canonical. `src/app/robots.ts` and
  `src/app/sitemap.ts` (`dynamic = "force-static"`) generate `robots.txt` and
  `sitemap.xml` into `out/` from `SITE_URL`; add each page to the sitemap's
  `routes`. The **Vite** starter does the same with `forgeSiteFiles({ siteUrl:
  process.env.SITE_URL })` in `vite.config.ts` (`@reliantlabs/forge-web-runtime/vite`):
  `robots.txt` plus a `sitemap.xml` of every `.html` file in `dist/`; list
  client-side routes with its `routes` option. A file you put in `public/`
  is never overwritten. With `SITE_URL` empty, `robots.txt` has no
  `Sitemap:` line and there is no sitemap.
- **`public/favicon.svg` and `public/og.png`** (1200×630) are neutral
  placeholders carrying the text `forge:placeholder-image`, so the audit
  reports them (`og_image_placeholder`) until you replace them with the
  site's own.
- **`npm run lint`** runs eslint with the jsx-a11y rules every forge frontend
  shares (`eslint.a11y.mjs`): a control with no accessible name, no keyboard
  path or a lying role is an error. Your own rules go after the spread in
  `eslint.config.mjs`.
- **No `404.html` in the Vite starter.** It is a single-page app: every
  unknown path serves `index.html` with a 200, so a client-side router can
  take it. The audit reports that as a soft 404 (info for an SPA). Add a
  `404.html` only once the site has no client-side routes, or it breaks them.
  A Next.js export writes its own `404.html`.

forge never rewrites a site's files. An adopted site (`--in-place`) gets the
`SITE_URL` declaration in its envs and keeps its own head, robots.txt and
lint config: read `process.env.SITE_URL` in its build to use it. The audit
names each gap with its fix.
