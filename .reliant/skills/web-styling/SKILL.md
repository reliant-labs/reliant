---
name: web-styling
description: The web UI styling contract — semantic Tailwind tokens, color-scheme/dark-mode selectors, when inline styles and !important are allowed, and the elevation rule (bg-background recesses, bg-muted does not). Load before changing any styling or layout under web/src.
---

# Web styling contract

Use the standard Tailwind + CSS variable token path for UI styling:

- Prefer semantic Tailwind classes such as `bg-background`, `text-foreground`, `text-muted-foreground`, `border-border`, `bg-primary`, `text-primary-foreground`, `text-destructive`, and component variants over inline token styles.
- Use `cn()` for conditional classes; do not mutate DOM styles in hover/focus handlers when Tailwind state variants can express the state.
- Theme palettes are controlled by `data-color-scheme`; light/dark mode is controlled by the `.dark` class. Do not add new `data-theme` selectors.
- Keep new shared styling in component primitives under `web/src/components/ui/` or token variables in CSS, not deep ad-hoc selector chains.
- For workflow config panel styles, add explicit `cpv2-*` ownership classes at the rendered component boundary instead of overriding shared primitives or targeting nested generated markup.
- Inline styles are acceptable for runtime geometry, Electron drag regions (`WebkitAppRegion`), dynamic SVG/data colors, and third-party APIs that require style objects.
- Avoid broad `!important`, magic negative margins, incidental parent selectors, hardcoded brand colors, and arbitrary CSS values unless the exception is documented near the use.
- `!important` is acceptable only for browser quirks or generated third-party DOM such as WebKit autofill, Monaco, XTerm, and ReactFlow; keep selectors scoped and prefer component props/classes first.
- Run `npm run lint:css` from `web/` when changing stylesheets; keep the Stylelint config focused on correctness/hygiene rather than formatting churn.
- When changing tokens or color-scheme CSS, run the color-scheme contract test and a web build path when practical.

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
and dark across all seven schemes in `web/src/themes/professional-themes.css`:
in dark it is *lighter* than `--card` (pure-black: background 0%, card 10%,
muted 15%), in light it is *darker* (background 98%, card 100%, muted 94–96%).
So the same class recesses on one theme and lifts on the other, and a
`bg-muted/40` inset reads as a well to whoever built it and as a faint floating
slab to everyone on the other mode. That is exactly how the billing surfaces
ended up as a stack of near-identical near-black rectangles. `bg-background` is
the only token that recesses in BOTH modes.

The border is load-bearing, not decoration: light mode gives the inset only two
points of luminance, so `border-border/60` is what makes the well read at all.

The long version, including the heading ladder (page heading / panel heading /
section label / stat caption) these surfaces follow, is the comment block at
the top of `web/src/components/forge-ui/card.tsx` — forge's component-library
card, installed verbatim (the rule was upstreamed into forge so there is one
copy). Use its `CardInset` for a well; forge's `surface-sunken` token is bridged
to `--background` in `web/src/index.css`, whose MUTED-INVERSION TRAP block has
the per-scheme numbers.
