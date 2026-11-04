---
name: web-styling
description: The web UI styling contract — semantic Tailwind tokens, the brand roles (blue acts, violet labels, green succeeds), accent text in text-primary-ink never text-primary, one focus ring, Geist type, color-scheme/dark-mode selectors (Reliant is the default), when inline styles and !important are allowed, and the elevation rule (bg-background recesses, bg-muted does not). Load before changing any styling or layout under frontends/web/src.
---

# Web styling contract

Use the standard Tailwind + CSS variable token path for UI styling:

- Prefer semantic Tailwind classes such as `bg-background`, `text-foreground`, `text-muted-foreground`, `border-border`, `bg-primary`, `text-primary-foreground`, `text-primary-ink`, `text-destructive`, and component variants over inline token styles.
- **Exception: `frontends/web/src/components/Forge/**` (the Apps console) speaks forge-ui's vocabulary** — `text-ink`, `text-ink-muted`, `bg-surface`, `bg-surface-sunken`, `*-danger-*` — not reliant's `text-foreground` / `bg-card` / `bg-background` / `*-destructive*` / `bg-muted`. `components/Forge/__tests__/tokenVocabulary.test.ts` fails on the reliant names there. `primary`, `ring` and `accent` stay as they are (a portalled dialog leaves the `.forge-ui` scope that remaps `accent`).
- Use `cn()` for conditional classes; do not mutate DOM styles in hover/focus handlers when Tailwind state variants can express the state.
- Theme palettes are controlled by `data-color-scheme`; light/dark mode is controlled by the `.dark` class. Do not add new `data-theme` selectors.
- Reach for forge's component library first: installed components live in `frontends/web/src/components/forge-ui/` (`forge component install <name>` writes there, per the `"forge"` block in `frontends/web/package.json`), are kept verbatim, and render inside a `.forge-ui` ancestor — outside it forge's `accent` paints as reliant's muted tint (see the ACCENT COLLISION note in `frontends/web/src/index.css`). Every primitive the app uses is a forge-ui install (`components/ui/` is gone: Tooltip, ContextMenu, Combobox and Popover were the last, in R5); a composition over them that only one feature uses lives in that feature's directory (`workflow/HelpPopover.tsx`), and a primitive forge lacks is built in forge first. Keep new shared styling there or in token variables in CSS, not deep ad-hoc selector chains.
- For workflow config panel styles, add explicit `cpv2-*` ownership classes at the rendered component boundary instead of overriding shared primitives or targeting nested generated markup.
- Inline styles are acceptable for runtime geometry, Electron drag regions (`WebkitAppRegion`), dynamic SVG/data colors, and third-party APIs that require style objects.
- Avoid broad `!important`, magic negative margins, incidental parent selectors, hardcoded brand colors, and arbitrary CSS values unless the exception is documented near the use.
- `!important` is acceptable only for browser quirks or generated third-party DOM such as WebKit autofill, Monaco, XTerm, and ReactFlow; keep selectors scoped and prefer component props/classes first.
- Run `npm run lint:css` from `frontends/web/` when changing stylesheets; keep the Stylelint config focused on correctness/hygiene rather than formatting churn.
- When changing tokens or color-scheme CSS, run the contract tests in `src/components/Settings/__tests__/` — `colorSchemeTokenContract` (every forge token resolves in every scheme, light and dark, and forge Button labels stay AA), `mutedForegroundContrast`, `statusInkContrast`, `primaryInkContrast` and `reliantSchemeDefault` (the default, the site palette, elevation order, brand violet) — and the screenshot pass `e2e/color-schemes.spec.ts` against a dev server (`BASE_URL=<your vite> npx playwright test e2e/color-schemes.spec.ts`; `--update-snapshots` when a pixel change is intended, then review the image diff).

## Brand roles

The app shares the site's brand (landing-page `tailwind.config.js`). Each hue
has one job:

- **Blue is action, link, focus and live state.** The fill is `bg-primary`
  (`#0a70e0` under the Reliant scheme, a white label at 4.8:1), its hover
  `--primary-hover`, focus `ring-ring` (`#0d8cff`), and blue TEXT is
  `text-primary-ink` (`#5cb0ff` in dark). `--info` is the "running / live" blue.
  One filled primary button per view; everything else is ghost or outline.
- **Violet is labels only, never interactive.** `text-brand-violet` is for at
  most one section eyebrow per screen, and only on marketing-like surfaces
  (onboarding, a first-run empty state, the plan picker). It is never a link,
  a button, a selection or a status. The raw logo violet (`#6e39ff`) is 3.5:1
  on the dark canvas, so it is decoration only (the mark carries it).
- **Blue to violet is one brand moment per screen**, or none. No rainbow or
  multi-hue gradients on buttons, steppers, cards or headings.
- **Green is success only.** A stepper, a progress bar or an "active" chip is
  not a success: use blue, or a neutral.
- **No other hues in chrome.** Raw palette classes (`bg-blue-500`,
  `text-purple-400`, `from-emerald-400`, hex literals) are a violation outside
  data visualization. Charts, syntax highlighting and diagram node types use
  data tokens, not the brand or status colors.

### Accent text: `text-primary-ink`, never `text-primary`

`--primary` is a FILL, tuned for the label on it. As text it can fail AA (the
brand blue fill is 3.9:1 as text on a dark panel), so accent text — a link, an
active label, an accent icon, a count — paints in `--primary-ink`
(`text-primary-ink`, `hsl(var(--primary-ink))`), which every scheme holds at
4.5:1 on every surface and on `bg-primary/10`–`/20` tints.
`primaryInkContrast.test.ts` fails on any bare `text-primary` or inline
`color: hsl(var(--primary))`. Prefer `text-foreground` plus a blue indicator
(an icon, a bar, a dot) over turning a whole label blue: "selected" is not a
link.

Selection and hover are an ink wash (`bg-foreground/5`), not a primary tint:
blue stays on the action.

### Focus: one recipe

The global `*:focus-visible` outline is a solid 2px `--ring` with a 2px gap.
A component that draws its own ring uses the same color at full strength:
`focus-visible:ring-2 focus-visible:ring-ring` (add `ring-offset-2
ring-offset-background` where the gap reads). Never an alpha step of the ring
(`ring-ring/40`), and never `ring-primary` or `ring-accent` (outside
`.forge-ui`, `accent` is the muted tint: a grey ring). `focusRingContract.test.ts`
enforces it outside `components/forge-ui/` (installed verbatim from forge).

### Labels and type

- Shell and panel labels are sentence case, `font-medium`, muted. No
  `uppercase tracking-wide` group headers (the sidebar, mobile section labels);
  the one exception is the violet eyebrow above.
- The UI face is **Geist**, the mono **Geist Mono** (`@fontsource-variable`,
  imported in `main.tsx`; `--font-sans` / `--font-mono`, with a metric-matched
  `Geist Fallback`). Change type through those tokens, never per-component
  `font-[...]`. JetBrains Mono stays the editor and terminal face
  (`MONACO_FONT_FAMILY`); Inter is an Appearance option.

## Color schemes

**Reliant is the default for everyone** (`DEFAULT_COLOR_SCHEME` in
`src/lib/forgeTokens.ts`; `index.html`'s bootstrap repeats it and a test holds
the copies equal). The other schemes stay selectable. Its dark ramp is the
site's: canvas `#0a0a0f` (`--background`), panel `#111118` (`--card`), raised
panel `#18181f` (`--popover`), hover/selected `--muted` at 14%, the hairline
border as its opaque equivalent on a panel (HSL tokens cannot carry the site's
alpha). Light is derived (the site is dark-only). A scheme is one
`:root[data-color-scheme=…]` and one `.dark[…]` block in
`themes/professional-themes.css`, a row in `ColorSchemeSelector.tsx` and an id
in `COLOR_SCHEMES` / `COLOR_SCHEME_ATTR`; it must declare `--primary-ink`. A
retired id maps onto its replacement in `COLOR_SCHEME_ATTR` and
`RETIRED_COLOR_SCHEME_IDS` (the old default `black` maps to Reliant; Pure
Black is `pure-black`).

Do not import the site's `inset` (`#06060a`, darker than the page) as a token,
or its legacy `surface.50–400` ramp: an inset is `bg-background` (below).

## Elevation: `bg-background` recesses, `bg-muted` does not

For nesting a panel inside another panel, use:

```
page                     bg-background
primary surface          bg-card + border-border
inset inside a surface   bg-background + border-border/60
```

Never nest a card inside a card, and **never reach for `bg-muted` to build
structure** — it is interaction state (hover, selected, disabled) only.

The reason is not taste. `--muted` moves in OPPOSITE directions between light
and dark across every scheme in `frontends/web/src/themes/professional-themes.css`:
in dark it is *lighter* than `--card` (reliant: background 4.9%, card 8%,
muted 14%), in light it is *darker* (background 98%, card 100%, muted 94–96%).
`reliantSchemeDefault.test.ts` holds that order in every scheme.
So the same class recesses on one theme and lifts on the other, and a
`bg-muted/40` inset reads as a well to whoever built it and as a faint floating
slab to everyone on the other mode. That is exactly how the billing surfaces
ended up as a stack of near-identical near-black rectangles. `bg-background` is
the only token that recesses in BOTH modes.

The border is load-bearing, not decoration: light mode gives the inset only two
points of luminance, so `border-border/60` is what makes the well read at all.

The long version, including the heading ladder (page heading / panel heading /
section label / stat caption) these surfaces follow, is the comment block at
the top of `frontends/web/src/components/forge-ui/card.tsx` — forge's component-library
card, installed verbatim (the rule was upstreamed into forge so there is one
copy). Use its `CardInset` for a well; forge's `surface-sunken` token is bridged
to `--background` in `frontends/web/src/index.css`, whose MUTED-INVERSION TRAP block has
the per-scheme numbers.
