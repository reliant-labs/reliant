---
name: ui-native-package
description: The `@<scope>/ui-native` workspace package — what ships, the web/native gap, ownership rules, and when to outgrow it for Tamagui or Unistyles.
---

# UI Native Package

## What this is

`@<scope>/ui-native` is a deliberately small React Native primitive set
that forge scaffolds into `packages/ui-native/` when:

1. The project uses the pnpm-workspaces layout (`pnpm-workspace.yaml` at the root), AND
2. At least one frontend is a React Native app.

It is **not a design system**. It is a thin layer of consistent
primitives so the scaffolded RN app can use named components
(`<Button variant="primary">`) instead of hand-rolling Pressable +
StyleSheet at every screen.

If neither precondition holds the package is never written — projects
that don't have an RN frontend stay clean.

## What ships (10 primitives)

| Primitive | Source | Props |
| --------- | ------ | ----- |
| `Button` | `components/button.tsx` | `variant: primary \| secondary \| outline \| ghost \| danger`, `size: sm \| md \| lg`, `fullWidth`, `isLoading`, `onPress`, `disabled` |
| `Input` | `components/input.tsx` | `inputSize`, `invalid`, `label`, `errorText`, `required`, all TextInput props |
| `Label` | `components/label.tsx` | `required`, all Text props — standalone label for non-Input controls |
| `Card` | `components/card.tsx` | `padding: none \| sm \| md \| lg` — bordered surface with iOS shadow + Android elevation |
| `Stack` | `components/stack.tsx` | `direction`, `gap` (spacing key), `align`, `justify`, `wrap`; also exports `HStack`, `VStack` |
| `Text` | `components/text.tsx` | `size`, `weight: regular \| medium \| semibold \| bold`, `tone: default \| muted \| primary \| destructive` |
| `Spinner` | `components/spinner.tsx` | All ActivityIndicator props; defaults color to active palette's primary |
| `Switch` | `components/switch.tsx` | All RN Switch props; palette-aware track + thumb colors |
| `Pressable` | `components/pressable.tsx` | All Pressable props, plus `pressedOpacity` (default 0.7) |
| `SafeAreaView` | `components/safe-area-view.tsx` | All `react-native-safe-area-context` SafeAreaView props with palette background default |

Plus `tokens.ts` — `colors.light` / `colors.dark`, `spacing` (4/8/12/16/20/24/32/40/48), `radius`, `textSizes`.

## Honest platform differences

| Web library | Native equivalent | Note |
| ----------- | ----------------- | ---- |
| `onClick` | `onPress` | RN convention. Don't fight it. |
| `className` (Tailwind) | `style` (StyleSheet/inline) | Inline objects are fine for primitives. |
| `<button type="submit">` | n/a | RN has no forms-with-action-buttons concept. |
| `disabled: opacity` via CSS | `Pressable` style fn returns opacity | We do this for you in Button + Pressable. |

## What is NOT ported (and why)

These web library components have no `ui-native` equivalent:

- **DataTable, FilterBar, Pagination** — virtualized tables on RN need
  `FlatList` + a real mobile pattern (swipe rows, pull-to-refresh).
  Hand-rolling a `<Table>` primitive on top of `<View>` produces an
  unusable result. Use `FlatList` directly, or install a library that
  understands mobile UX.
- **Sidebar, NavHeader, SidebarLayout** — mobile navigation is tabs +
  stack, not a sidebar. Use `expo-router`'s built-in Tabs / Stack /
  Drawer.
- **Modal, Dialog, ConfirmationDialog** — RN ships a `Modal` component
  but the design-language difference between an iOS action sheet, an
  Android dialog, and a custom bottom sheet is large. Pick one
  per-screen rather than shipping a "one-size-fits-all" Modal.
- **Tabs, DropdownMenu, CommandBar** — same: these are platform-shaped
  controls and the RN ecosystem has dedicated libs (e.g.
  `react-native-tab-view`, native-stack screens).
- **Charts, Diagrams** — Recharts and the bespoke deck/diagram
  components are SVG-DOM. RN has `react-native-svg` but the rendering
  cost on mid-tier Android is enough that you should pick a charting
  library matched to your specific data shape.
- **All marketing components** (hero_section, pricing_table,
  testimonial_cards, …) — these are landing-page shapes, not in-app
  surfaces.

## Ownership

`forge generate` writes every file under `packages/ui-native/` **once**
(write-if-missing). After that, edits survive every regen. Concretely:

| Path | Owner after first write |
| ---- | ----------------------- |
| `packages/ui-native/package.json` | You |
| `packages/ui-native/tsconfig.json` | You |
| `packages/ui-native/src/tokens.ts` | You |
| `packages/ui-native/src/index.ts` | You |
| `packages/ui-native/src/components/*.tsx` | You |
| `packages/ui-native/README.md` | You |

To re-emit a primitive from the embedded forge source (e.g. you want
to revert a local edit to `button.tsx`):

```bash
rm packages/ui-native/src/components/button.tsx
forge generate
```

## When to outgrow this

The point at which you should reach for a real design system rather
than extending `ui-native`:

- You want **one component library across web AND native** (write
  `<Button>` once, render on both platforms).
- You need **runtime theme switching**, brand variants, or a
  token graph (primary-dark, primary-hover, primary-pressed, …).
- You need DataTable / Sidebar / NavHeader equivalents that work on
  mobile.
- You want **animation primitives** (Reanimated bindings, layout
  transitions) baked in.

Two paths forward:

### Tamagui

[Tamagui](https://tamagui.dev) compiles down to native StyleSheet on
RN and CSS-in-JS on web from one component definition. Migration
shape:

```bash
cd packages/ui-native
pnpm add tamagui @tamagui/config react-native-reanimated
```

Replace `colors` / `spacing` / `radius` in `tokens.ts` with a Tamagui
config object, then rewrite each primitive as a Tamagui `styled()`
component. Because everything was a thin RN primitive, the
search-and-replace is mechanical: `Pressable` → `styled(Pressable, {...})`,
inline `style={…}` → variant props.

### Unistyles

[react-native-unistyles](https://www.unistyl.es/) is a lighter-weight
option that adds theming + responsive breakpoints + variants on top of
StyleSheet, but stays RN-only. If you don't need a unified web library
this is the smaller migration.

Either way: `ui-native` was scaffolded as a one-time copy so you can
delete it freely once you've replaced the surface.

## Why not just ship Tamagui by default?

- **Lock-in.** Tamagui is opinionated; not every team wants the
  compile-time component model.
- **Build complexity.** Tamagui's compiler is an extra babel + metro
  setup step that adds friction to a fresh scaffold.
- **Scope.** Forge's job is the scaffolding seam — once that seam
  exists, picking a design system is a project decision, not a
  generator one.

The thin primitive set keeps the seam working out-of-the-box so an
agent can ship a screen on day 1 without picking a design system.

## See also

- `frontend-workspaces` skill — the workspace layout this package
  lives inside.
- `frontend` skill — single-frontend dev loop.
- `components` skill — the web component library (DOM/Tailwind);
  there is intentionally no overlap with `ui-native`.
