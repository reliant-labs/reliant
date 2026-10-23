# public/

Static assets served verbatim at the site root. Anything placed here is
reachable at `/<filename>` with no build step (e.g. `public/logo.svg` →
`https://your-host/logo.svg`).

Good candidates:

- `favicon.ico`, `apple-touch-icon.png`
- `robots.txt`, `sitemap.xml`
- Pre-built `og-image.png` for social previews
- Static fonts or icons that don't need hashing

Don't put anything secret or anything you'd want cache-busted here — use
`import`'d assets from `app/` for those. See the
[Next.js static assets docs](https://nextjs.org/docs/app/building-your-application/optimizing/static-assets)
for the full set of rules.
