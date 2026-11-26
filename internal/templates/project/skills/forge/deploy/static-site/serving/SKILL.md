---
name: deploy/static-site/serving
description: How Reliant hosted static hosting serves a site's build — how a path resolves to a file, 404.html versus the SPA fallback, response headers from `_headers`, path redirects from `_redirects` (Netlify-subset syntax, `:splat`, `!` force, what fails a deploy, unclaimed sites), and the asset cache policy.
---

# How hosted static hosting serves a build

## How hosted static hosting resolves a path

A request for `/books/view?id=…` is served `books/view.html`. The export
writes one `.html` file per route because `trailingSlash` is off, and the
hosted origin tries `<path>`, then `<path>.html`, then `<path>/index.html`.
Every link the scaffold generates resolves to the file for its own route.
`frontend/serving` covers hosts that only map directories to `index.html`.

What a path no file answers gets is declared by the build itself, by
Cloudflare Pages' rule:

- **The build ships a `404.html`** at the site's root (a Next.js export
  always does; a Vite site adds `public/404.html`): every miss is that page
  with status **404** and `Cache-Control: no-store`. There is no SPA
  fallback, so every page must be a file — a static export, or a SPA that
  prerenders each route as `<path>.html`. This is what keeps a mistyped URL
  out of search results as a copy of the homepage.
- **No `404.html`**: the site is a single-page app. A route-shaped miss
  falls back to the root `index.html` with 200 for the client router to
  draw; a miss with an extension is a plain 404.

## Response headers: `_headers`

A `_headers` file at the root of the build (Vite: `public/_headers`; Next.js:
`public/_headers`) declares response headers, in the Netlify / Cloudflare
Pages format. It is part of the release, so the policy ships with the code it
describes and a promoted digest serves the same headers in every env. The
file itself is never served.

```
# every response
/*
  Content-Security-Policy: default-src 'self'; frame-ancestors 'none'
  Permissions-Policy: camera=(), microphone=(), geolocation=()
  X-Frame-Options: DENY
# a narrower rule adds to (and, for a repeated name, joins with) the wider one
/embed/*
  Content-Security-Policy: frame-ancestors https://partner.example
```

- A pattern is an absolute request path; `*` matches anything, `/` included.
  `:placeholders`, per-host rules and `! Name` removal are not supported.
- Every matching rule applies, in file order; a name several rules set is
  joined with `, ` (two CSPs are both enforced, so a rule can only tighten).
- The platform always sends `X-Content-Type-Options: nosniff`,
  `Referrer-Policy: strict-origin-when-cross-origin` (yours replaces it) and
  `Strict-Transport-Security: max-age=31536000` (`includeSubDomains` is a
  per-domain flag: `deploy/domains`). It sends NO anti-framing header:
  framing is the site's policy. A scaffolded app frontend (Next.js, Vite)
  ships `public/_headers` with `frame-ancestors 'self'` and
  `X-Frame-Options: SAMEORIGIN`; a site created before that, or a site
  without the file, can be framed by any origin until it declares one.
- Refused, and logged: the headers the platform owns (`Set-Cookie`,
  `Cache-Control`, `Content-Type`, `Strict-Transport-Security`,
  `X-Content-Type-Options`, `Access-Control-Allow-Credentials`, HTTP
  message-framing headers such as `Content-Length` and `Transfer-Encoding`,
  and hop-by-hop headers), invalid names or values, and anything past the caps —
  100 rules, 50 headers per rule, 4 KiB per value, 64 KiB per file, 16 KiB of
  file headers per response. A bad line is skipped; the rest still apply.
- On an unclaimed preview, CORS stays the platform's (`*`) and the
  preview sandbox policy is added beside your CSP.

A missing file that is not route-shaped (`/assets/app-1a2b.js`) is a 404,
never the HTML fallback. The frontend's content-hashed asset directory
(`asset_dir`: `_next/static` for Next.js, `assets` for Vite) is published as
the site's `assetDir`: files under it are served
`public, max-age=31536000, immutable`, everything else `no-cache`, and the
previous releases' copies stay serveable after a deploy, so a tab opened
before it can still lazy-load the chunks it was built against.

## Redirects: `_redirects`

A `_redirects` file at the root of the build (`public/_redirects` in Vite and
Next.js) declares path redirects, in a subset of the Netlify / Cloudflare
Pages format. Like `_headers` it ships in the release, so a promoted digest
redirects the same way in every env, and it is never served.

```
# one rule per line: source  target  [status]
/docs          https://docs.example.com/        301
/docs/*        https://docs.example.com/:splat  301
/old-pricing   /pricing
/beta/*        /app/:splat                      302!
```

- The source is an exact path (a trailing `/` on the request is ignored) or
  a prefix ending in `/*`, which also matches the prefix itself (`/docs/*`
  matches `/docs`). What `*` matched is `:splat` in the target.
- The target is a path on the site or an absolute `http(s)` URL; the
  request's query string is kept.
- Status is 301 (the default), 302, 307 or 308. The first matching rule wins.
- A published file at the same path wins over a rule, Netlify's shadowing
  rule, unless the status ends in `!` (`301!`). So a site that moves
  `/docs` away must also stop building `docs.html` / `docs/index.html`, or
  force the rule.
- 301 and 308 are cached `public, max-age=3600` (not forever, so deleting a
  rule reaches browsers within the hour); 302 and 307 are `no-cache`.
- Not supported, and **a deploy with such a line fails** with each bad line
  named (it is not silently dropped): `200` rewrites and proxying (the one
  exception, `/* /index.html 200`, is accepted as a no-op: it is already
  the SPA fallback), `404`/`410` rules, placeholders other than `:splat`,
  query / country / language / role conditions, and per-host rules. At most
  1000 rules, 2048 characters a line, 64 KiB a file.
- Hostname-to-hostname redirects (www → apex) are a domain binding, not this
  file: `forge domain bind <host> --env <env> --redirect-to <target>` (`deploy/domains`).
- On an unclaimed preview (`--claimable`), a rule to another hostname is not
  followed; paths on the site still are. Once the site is claimed, the same
  release's external rules apply with no redeploy.
- A site on its own bucket or Firebase is not served by the platform origin,
  so the file does nothing there; Netlify and Cloudflare Pages read it
  natively.
