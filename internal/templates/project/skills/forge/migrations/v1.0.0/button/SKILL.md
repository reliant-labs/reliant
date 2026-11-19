---
name: migration-v1.0.0-button
description: Adopt the v1.0.0 library Button — reinstall it with forge component install button, rename isLoading to loading, re-pick sizes on the denser xs–xl scale (old sm/md/lg were 32/40/48px; new sm/md/lg/xl are 28/32/36/40px), and replace hand-rolled icon slots, icon-only buttons and link-styled anchors with leftIcon/rightIcon, icon + aria-label, and href.
relevance: migration
---

# v1.0.0 — the library Button

Use when a frontend's installed `button.tsx` (in `src/components/ui/`, or
the directory its package.json `"forge"` block names in `components_dir`)
still declares `isLoading?: boolean`.

## What changed

`button` is `overwrite: once`: forge wrote it into the project once and never
touches it again, so an existing project keeps the old component until it
reinstalls. The new one:

| | before | v1.0.0 |
|---|---|---|
| loading prop | `isLoading` (disabled, spinner pushed the label) | `loading`: spinner, **same width**, `aria-busy`, disabled |
| sizes | `sm` 32 / `md` 40 / `lg` 48px | `xs` 24 / `sm` 28 / `md` 32 / `lg` 36 / `xl` 40px |
| icons | hand-placed children | `leftIcon` / `rightIcon`, sized by the button |
| icon-only | `className` overrides + `aria-label` by convention | `icon={...}` with a **type-required** `aria-label` |
| focus | `ring-2` (box-shadow; gone in forced-colors mode) | 2px `outline` 2px off the edge, `:focus-visible` only |
| pressed | none | one step past hover plus a 1px settle; `aria-expanded` / `aria-pressed` hold it |
| as a link | wrap in `<a>` or copy the classes | `href` renders through `./link`; `buttonClasses()` (from `./button_classes`) for any other element |
| `ref` | not forwarded | forwarded (`React.forwardRef`) |

Variant names are unchanged: `primary`, `secondary`, `outline`, `ghost`,
`danger`. The default is still `primary` at `md` — but `md` is now 32px, the
dense console height (Stripe's dashboard, Linear, GitHub's Primer), not 40px.

## What to do

1. **Reinstall the component.** From the frontend directory:

   ```bash
   reliant forge component install button
   ```

   It writes `button.tsx` into the components directory and installs `link.tsx`
   beside it if missing (the button's `href` form imports it). It overwrites
   `button.tsx`: if you edited your copy, diff first and carry the edit into a
   wrapper component rather than into the file.

2. **Rename `isLoading` to `loading`** at every call site. The new type
   rejects `isLoading`, so `tsc` lists them all:

   ```bash
   grep -rl --include='*.tsx' 'isLoading=' src | xargs sed -i 's/\bisLoading=/loading=/g'
   ```

   Review the result: the sed also matches a non-Button component's
   `isLoading` prop. Then drop any `disabled={pending}` that only duplicated
   `loading={pending}` — `loading` already disables.

3. **Re-pick sizes where height matters.** A button beside an `Input` of the
   default size (40px) wants `size="xl"`; a form's full-width submit too.
   Toolbar and table buttons usually look right at the new defaults. The
   mapping that preserves the old heights exactly is `sm → md`, `md → xl`, and
   `lg → xl` (no size is 48px any more; a hero CTA can add `className="h-12"`).

4. **Use the slots.** Replace `<Button><PlusIcon className="size-4" /> New</Button>`
   with `<Button leftIcon={<PlusIcon />}>New</Button>` (the button sizes the
   icon), an icon-only button with `<Button icon={<XIcon />} aria-label="Close" />`,
   and `<a className="…button classes…">` with `<Button href="/x">`.

## Verify

`npm run typecheck` is clean (no `isLoading`, no unknown size). Tab through a
page: every button shows a 2px accent outline offset from its edge, and a
loading button does not change width.
