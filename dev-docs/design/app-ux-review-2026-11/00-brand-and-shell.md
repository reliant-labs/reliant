> Copied on 2026-11-01 from `research/app-ux-review-2026-11/00-brand-and-shell.md` (outside any repository) so it is versioned with the synthesis.

# 00 — Brand tokens, type and the global shell

Scope: how the app's styling system maps onto the landing page's (landing-page
`origin/main` d6f49ac, `tailwind.config.js`). Covers the `web-styling` contract
changes, the shell (header, sidebar, nav), and the type, radius and spacing
scales. Per-area findings are in 01–06.

Evidence: reliant `origin/main` 0ec4b8119, a production build in the fixture
harness (`HARNESS.md`), Chrome DevTools at 1440×900 and 390×844, both modes.
**Measured, not seen.** `take_screenshot` without `filePath` returned a text
placeholder (bug 3f6a8b8e). A saved PNG opened as metadata only. Everything
below comes from computed styles, contrast math over composited backgrounds,
the DOM and the source. The owner should check it on the 5315 preview.

## Where the app stands today

| | App today (default: dark, `pure-black`) | Landing |
|---|---|---|
| Page canvas | `#000000` (`--background 0 0% 0%`) | `#0a0a0f`, a blue-violet near-black |
| Panel | `#121212` (`--card 0 0% 7%`) | `#111118` |
| Raised / popover | `#1c1c1c` (`--popover 0 0% 11%`) | `#18181f` |
| Border | `#2e2e2e` solid (`--border 0 0% 18%`), 1.38:1 against the card | `rgb(220 224 255 / .1)` hairline, about `#25262f` on a panel (1.25:1) |
| Primary action | near-white fill `#fafafa`, black label | blue `#0a70e0` fill, white label (4.8:1) |
| Links / live state | white or muted grey | blue `#0d8cff` (5.8:1 on canvas) |
| Focus ring | white at 40–90% alpha (`--ring 0 0% 98%`) | solid `#0d8cff` with a canvas gap (5.6:1 on panel) |
| Labels / eyebrows | grey, often `uppercase tracking-wide` (186 uses in 125 files) | violet-300 `#a58cff`, only on section eyebrows |
| Brand hue anywhere in the default chrome | **none**: the mark appears only on auth screens (`ApiKeyLogin.tsx`, `ProxyAuth.tsx`) | the mark, the CTA, links, focus, one glow |
| UI font | Inter (bundled, `public/fonts/Inter-roman.woff2`) | Geist Variable (self-hosted) |
| Mono | JetBrains Mono | Geist Mono |
| Radius | `--radius .5rem` (7.5px at the 15px root): buttons 5.5px, composer 7.5px | buttons `rounded-full`, cards `rounded-2xl` (16px) |
| Base size | root 15px (`DEFAULT_FONT_SIZE = "lg"`); `text-sm` 14.1px, `text-xs` 12.2px | body 16px |

Two products, one brand, and today they share nothing visible except the
favicon. The default scheme is a well-built monochrome UI with no brand in it.
The landing is near-black with blue as the only interactive colour. The gap is
mostly **palette and font**. Structure, density and the elevation rules are
already sound, and the app is better tested than the site: three contrast
contract tests hold every scheme.

What already matches and should stay:
- Dark by default. Near-black surfaces, light borders, no shadows on panels
  (`forge-ui/card.tsx` elevation rule).
- Green means success. `--success` is green and the scheme set was pruned on
  2026-10-24 to stop action colours colliding with status colours.
- Contrast. On the default home screen the only text under 4.5:1 is the
  decorative `/` breadcrumb separator (2.8:1 dark, 2.0:1 light). Muted text is
  7.9:1 on cards.
- Focus. Every shell control tabbed through has a visible indicator, a 2px
  outline or ring. The sidebar's ring is `ring/40`, about 3.7:1 on `#121212`:
  it passes, but it is faint.
- Density. Header 45px, sidebar 239px, nav rows 30px. That is Linear/Vercel
  dashboard density, and it is right for an app.

## Recommendation: a "Reliant" scheme, made the default

Add a seventh colour scheme, **Reliant**, built from the landing tokens, and
make it the default for new users. Keep **Pure Black** for OLED. Do not
re-skin pure-black in place: people chose it by name.

This is the lowest-risk path because the machinery already exists. A scheme is
one `:root[data-color-scheme=…]` and one `.dark[…]` block in
`frontends/web/src/themes/professional-themes.css`, a row in
`ColorSchemeSelector.tsx`, a key in `COLOR_SCHEME_MAP` / `lib/forgeTokens.ts`,
and the contract tests pick it up. The default lives in three places that must
change together: `index.html`'s pre-hydration bootstrap
(`localStorage … || 'black'`), `services/settingsSync.ts:401`, and
`hooks/useThemeInitialization.ts:61`.

### Token mapping (dark)

HSL triplets, the format of every token in `professional-themes.css`.

| App token | Landing source | Value | Notes |
|---|---|---|---|
| `--background` | `canvas` #0a0a0f | `240 20% 4.9%` | |
| `--card` | `panel` #111118 | `240 17% 8%` | |
| `--popover`, `--surface-modal` | `panel.raised` #18181f | `240 13% 10.8%` | |
| `--muted`, `--accent`, `--secondary` | (none; hover/selected only) | `240 11% 14%` | Must stay lighter than `--card` in dark (the inversion rule) |
| `--foreground`, `--card-foreground` | `fg` #f4f4f5 | `240 5% 96%` | 17.1:1 on panel |
| `--muted-foreground` | `fg.muted` #9d9da6 | `240 5% 63%` | 7.0:1 on panel |
| (new) `--body-foreground` | `fg.body` #b9b9c1 | `240 6% 74%` | Optional third text step for long prose (chat, docs) |
| `--border` | `hairline` on a panel | `234 12% 16.5%` | Opaque equivalent of 10% lavender-white over `#111118`. The token is an HSL triplet with `<alpha-value>`, so a literal alpha hairline cannot be expressed. Composite it. |
| `--config-input-border`, `--input` | `hairline.strong` | `234 9% 21%` | |
| `--primary` | `brand.blue.600` #0a70e0 | `211 92% 46%` | White label 4.8:1 |
| `--primary-hover` | `brand.blue.700` #0961c4 | `212 91% 40%` | A real colour, not an alpha (the file's own rule) |
| `--primary-foreground` | white | `0 0% 100%` | |
| `--ring` | `brand.blue` #0d8cff | `209 100% 52.5%` | 5.6:1 on panel. Restyle focus as the landing's ring plus offset gap |
| `--info` | `brand.blue.300` #5cb0ff | `209 100% 68%` | "Live / running" state, which the landing also gives to blue |
| `--success`, `--warning`, `--destructive` | (unchanged) | | Green stays success-only |
| (new) `--brand-violet` | `brand.violet.300` #a58cff | `253 100% 77.5%` | 7.0:1 on panel. Section eyebrows only, never interactive |
| (new) `--brand-gradient` | `brand-gradient` | `linear-gradient(90deg,#0d8cff,#8f6bff)` | At most one use per screen |

### Light mode

The landing is dark-only, so light mode is derived, not copied. Keep the
existing light ramp (`page 98% < card 100%`) tinted to hue 240 at chroma ≤ 0.02.
Use `--primary #0a70e0` (4.8:1 on white, 4.6:1 on `#fafafa`),
`--primary-hover #0961c4`, and `--ring #0a70e0`. Light violet for labels:
`#5b2ee0` (6.9:1 on `#fafafa`); `#6e39ff` also passes at 5.5:1.

### The one real risk: `text-primary` is 504 call sites

`--primary` is both a fill and a text colour across the app: `text-primary`
504 uses, `bg-primary` 432, `bg-primary/NN` tints 277, `border-primary` 200,
`ring-primary` 92. In the default scheme, primary is near-white, so all of
those read as "strong neutral". In a blue scheme they all turn blue, which
cuts against "blue used sparingly". And `#0a70e0` as **text** on a dark panel
is **3.9:1**, which fails AA for body-size text.

Professional Blue avoided this by making dark `--primary` a light blue (70% L)
with a dark label. That is not the landing's white-on-blue button. The
durable fix is to split the token:

1. Add `--primary-ink` (dark `#5cb0ff`, 8.1:1 on panel; light `#0961c4`,
   5.7:1) and a `text-primary-ink` utility in `tailwind.config.js`.
2. Codemod `text-primary` (not `-foreground`) → `text-primary-ink`, then
   review the result. Many of the 504 are "selected / active" emphasis that
   should be `text-foreground` plus a blue indicator, not blue text.
3. Demote selection tints. Card.tsx's rule says hover and selection are an
   ink wash, so `bg-primary/10` selection fills become `bg-foreground/5–8`,
   and blue stays on the action, the link, the focus ring and live state.
4. Add a contract test: `--primary-ink` AA on background, card and popover in
   every scheme, beside `mutedForegroundContrast.test.ts`.

Until steps 1–3 land, a Reliant scheme with a blue `--primary` will look
blue-heavy and fail contrast on blue text. To preview the effect today, open
the preview with `?scheme=blue`: on the billing page, 13 visible elements
paint in `--primary`.

### Changes to the `web-styling` contract (`.reliant/skills/web-styling/SKILL.md`)

- **Brand roles.** Add a section mirroring the landing's comment block:
  blue = action, link, focus, live state. Violet = labels only, never
  interactive. Blue→violet only for one brand moment per screen. Green =
  success only. No other hues in chrome; diagrams and syntax keep data colours.
- **Raw palette classes.** Name them as a violation outside data
  visualisation. Today 55 files use raw `blue/purple/violet/green-NNN`
  classes and there are 129 hex literals in `.tsx`. The worst offenders against
  the brand rules:
  - `components/OnboardingFlow/ProgressBar.tsx:27-49`, an
    emerald→cyan→sky→violet gradient stepper (green as progress, and a
    rainbow).
  - `components/workflow/WorkflowEditorChatPanel.tsx:85`,
    `from-blue-500 to-purple-500` with `hover:scale-105`.
  - Violet as a node-type colour in `workflow/nodes/*`, which is data, so it
    can stay, but it should move to named `--node-*` data tokens.
  Add a Stylelint or ESLint rule, warn first.
- **Eyebrows vs. labels.** `forge-ui/card.tsx` says "never
  `uppercase tracking-wider`". The landing uses exactly that for its violet
  section eyebrows. Reconcile: sentence-case muted labels inside panels (the
  card.tsx ladder, unchanged), and at most one `SectionLabel`-style violet
  uppercase eyebrow, only above a page-level section on marketing-like
  surfaces (onboarding, empty states, plan picker). The sidebar's
  `ALL PROJECTS` / project group headers (`Layout/Sidebar.tsx:657`
  `font-semibold uppercase tracking-wide`) and mobile settings' `YOU` /
  `PREFERENCES` headers become sentence case.
- **Focus.** One recipe: `focus-visible:ring-2 ring-ring ring-offset-2
  ring-offset-background`, replacing per-component `ring-ring/40` and
  white outlines.
- **The `bg-muted` trap still applies, and the new scheme must keep it.** In
  dark, `--muted` (14%) must stay above `--card` (8%). The landing's `inset`
  (#06060a, below the canvas) maps to the app's existing rule: **an inset is
  `bg-background`, not a new token**. Do not import `inset` as
  `--surface-sunken`, a darker-than-page well. The bridge in `index.css`
  (`surface-sunken → --background`) is what keeps forge-ui components
  recessing in both modes. The landing's `surface.50–400` legacy ramp has no
  counterpart and should not be imported.
- **Accent collision.** forge's `accent` means "primary action" inside
  `.forge-ui` and reliant's `--accent` means "muted tint" outside it
  (`index.css` ACCENT COLLISION block). Introducing a blue primary makes a
  forge component rendered outside `.forge-ui` look *less* wrong (muted
  instead of white). That makes the bug easier to miss, not fixed. The scoping
  stays mandatory.

## Type

**Adopt Geist as the default UI face and Geist Mono for UI mono.** Keep Inter
and JetBrains Mono as Appearance options. Keep JetBrains Mono as the default
for the **terminal and editor** until Geist Mono's box-drawing and powerline
coverage is checked in xterm.

- Bundle `Geist-Variable.woff2` and `GeistMono-Variable.woff2` under
  `frontends/web/public/fonts/`, add `@font-face` beside Inter's
  (`index.css:627`), preload them in `index.html`, and point
  `--font-sans` / `--font-mono` (`index.css:416-417, 593`) at them. Add a
  metric-matched `Geist Fallback`, as the landing does, so the swap does not
  reflow.
- **Broken today:** Appearance → Font → "Geist" (`AppearanceSettings.tsx:397`,
  `:426`, `MobileAppearancePanel.tsx:54`) sets `font-family: 'Geist'`, but no
  Geist face is bundled. `document.fonts` holds only Inter, JetBrains Mono and
  codicon, so the "Geist" choice silently renders the platform font. Shipping
  the face fixes this as a side effect.
- **Scale.** The app's bumped, rem-based scale (`tailwind.config.js`
  `fontSize`) is good and tied to the Appearance size preference: keep it.
  Usage leans small: `text-xs` (12.2px at the default root) has 2,603 uses
  against 1,988 for `text-sm`. Sidebar chat titles render at 12.2px. Geist at
  12px reads lighter than Inter, so after the font swap move primary list
  content to `text-sm` (14.1px): chat titles, nav rows, settings row labels.
  Keep `text-xs` for metadata (timestamps, counts, captions). This meets the
  forge design floor (body ≥ 14px).
- **Headings.** Fourteen distinct `<h1>` class strings exist
  (`text-lg`/`xl`/`2xl`/`3xl`, `font-semibold`/`bold`, with or without
  `tracking-tight`). One rung: `PageHeader`
  (`text-xl font-semibold tracking-tight`, already used in 45 files), plus
  `text-balance`. Geist wants `tracking-tight` on every heading. The landing
  pairs `font-semibold tracking-tight leading-tight`. The big landing rungs
  (36–60px display) belong only on onboarding and first-run empty states.

## Radius and spacing

- **Keep `--radius .5rem` for app controls.** The landing's `rounded-full`
  buttons and `rounded-2xl` cards are marketing-scale. Vercel's own dashboard
  uses 6–8px controls beside a pill-button marketing site, and that split is
  correct here too.
- Use `rounded-xl` (about 11px) for the few large standalone panels that act
  like the site: onboarding cards, plan cards, the "Build an app" card on the
  new-chat screen, empty-state panels. Today radii are spread across
  `rounded-md` (728), `rounded-lg` (642), `rounded` (554), `rounded-sm` (82),
  `rounded-xl` (46) and `rounded-2xl` (14). Codify md for controls, lg for
  panels, xl for hero panels, full for pills and avatars.
- **Borders.** The composer has a 2px border at 70% (`composerWrap`, 640×102).
  It is the one 2px border in the shell. Make it a 1px hairline with a
  `--ring` border on focus-within, which is the landing's input treatment.
- **Spacing.** Keep card.tsx's scale (4/8/12/16/24/32; panel padding 16,
  panels 16 apart, sections 32). No change.

## The global shell

Measured at 1440×900, default scheme: header 45px on `#000`, sidebar 239px on
`#121212` (`--card`), main on `#000`.

1. **No brand in the chrome.** The Reliant mark never appears once you are
   signed in. Put the two-colour mark (16–18px, `assets/logo.svg`, already
   the brand file) at the top-left of the sidebar header, next to "Toggle Chat
   Sidebar". On macOS Electron, put it after the traffic-light inset
   (`Header.tsx` `windowAligned`). It should link home. This is the single
   cheapest change that makes the app look like it belongs to the site.
2. **Sidebar group headers shout.** `ALL PROJECTS` and the project group title
   are `text-xs font-semibold uppercase tracking-wide`, and the active one
   paints `text-primary` (`Sidebar.tsx:655-658`). Under a blue primary,
   "active project" would read as a link. Use sentence case, `font-medium`,
   `text-muted-foreground`, with `text-foreground` when active.
3. **Settings appears twice:** the header gear (`Header.tsx`) and the sidebar
   footer "Settings" button. Keep the sidebar one (it has a label). Drop the
   header gear, or turn it into the account/avatar menu that is missing today.
4. **Primary CTA.** In the default scheme "New chat" is a plain 30px nav row,
   the same weight as Apps, Inbox, Workflows and Search. The landing has
   exactly one blue CTA per view. Make "New chat" the shell's one filled
   primary button (blue under Reliant), and leave the rest as ghost nav rows.
5. **The new-chat screen has no headline.** Main shows the
   project/machine/workspace pickers, a "Build an app" card and the composer,
   with nothing that sets the tone. A single quiet line in the site's voice
   would tie it to the positioning ("You set the direction. Your agents do the
   work."), set in the `PageHeader` rung, `text-balance`. Copy for the owner
   to choose. The composer placeholder ("Ask Reliant, e.g. "Build a comment
   system"") is fine.
6. **Small hit targets in sidebar rows.** The per-row "New chat in this
   workspace" and "Archive" icon buttons are 19–21px, and Sort / View options
   are 23px. WCAG 2.2 SC 2.5.8 (AA) asks for 24×24 or equivalent spacing.
   Header buttons (26px) pass. Pad the icon buttons to 24px
   (`Sidebar.tsx` row actions).
7. **Mobile is a separate surface and it is in good shape.** Phones redirect
   to `/m/*` (`lib/mobileRedirect.ts`). At 390×844: no horizontal overflow
   (`scrollWidth` 390), **zero** controls under 44px in the chat list, root
   17px, heading `Chats` at 23.4px/600 with tight tracking. The only brand
   work there is the same token and font swap, plus the sentence-case section
   headers.

## Order of work

| # | Change | Effort | Files |
|---|---|---|---|
| B1 | Bundle Geist + Geist Mono, make them the default (also fixes the broken "Geist" option) | S | `public/fonts/`, `index.css` 416–417, 593–613, 627–645; `index.html` preloads |
| B2 | Mark in the sidebar header | S | `Layout/Sidebar.tsx` or `Layout/Header.tsx`, `assets/logo.svg` |
| B3 | Sentence-case shell labels; drop the duplicate settings gear | S | `Layout/Sidebar.tsx:655`, `Mobile/*Settings*`, `Layout/Header.tsx` |
| B4 | One focus recipe (blue ring with offset) | S–M | `index.css:929-940`, `forge-ui/button.tsx:151`, sidebar rows |
| B5 | `--primary-ink` split + `text-primary` codemod + selection tints → ink washes | M | `tailwind.config.js`, `themes/professional-themes.css`, ~500 `.tsx` sites, new contract test |
| B6 | "Reliant" scheme, default for new users | M (after B5) | `professional-themes.css`, `ColorSchemeSelector.tsx`, `lib/forgeTokens.ts`, `index.html`, `services/settingsSync.ts:401`, `hooks/useThemeInitialization.ts:61`, `e2e/color-schemes.spec.ts` snapshots |
| B7 | Brand-role rules in `web-styling` + lint for raw palette classes (warn first); fix the two gradients | M | `.reliant/skills/web-styling/SKILL.md`, `stylelint`/`eslint` config, `OnboardingFlow/ProgressBar.tsx`, `workflow/WorkflowEditorChatPanel.tsx` |
| B8 | Heading ladder through `PageHeader`; list content to `text-sm` | M | the fourteen `<h1>` variants; `Layout/Sidebar.tsx` rows |

Verification for B5/B6: `colorSchemeTokenContract.test.ts`,
`mutedForegroundContrast.test.ts`, `statusInkContrast.test.ts`, and
`BASE_URL=<vite> npx playwright test e2e/color-schemes.spec.ts --update-snapshots`
with a reviewed image diff (the `web-styling` skill's own rule).
