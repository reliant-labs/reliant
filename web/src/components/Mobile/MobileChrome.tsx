/**
 * Shared visual primitives for the `/m/*` screens: the navigation bar, the
 * grouped-card list container, and the empty state.
 *
 * These exist because the mobile surface previously drew every screen as
 * full-bleed rows separated by hairlines, with a 42px header whose title was
 * the same size as the labels beneath it. The result measured as zero rounded
 * and zero shadowed elements per screen — structurally a bordered table, not
 * an app. Centralising the treatment here is what keeps eight screens from
 * drifting apart again.
 *
 * ## Why `elevation-1` and not a hand-rolled shadow
 *
 * `elevation-1` (see `index.css`) already resolves `--surface-raised` and a
 * mode-appropriate shadow: in light mode the surface is a hair above the page
 * and the shadow does the separating; in dark mode `--surface-raised` is the
 * theme's `--muted`, a real lift that a shadow alone could not produce on a
 * near-black background. Both track `data-color-scheme`, so one class covers
 * ten color schemes times two modes.
 *
 * That same fact rules out `bg-muted` and `bg-accent` for anything drawn
 * *inside* a card — in dark mode they resolve to the card's own background and
 * vanish. Card-internal accents use `bg-primary/10`, and pressed states use
 * `bg-foreground/5`, both of which keep a visible delta against a raised dark
 * surface and a near-white light one.
 */

import type { ReactNode } from "react";
import type { LucideIcon } from "lucide-react";
import { ChevronLeft } from "lucide-react";
import { cn } from "../../lib/utils";

/**
 * Top navigation bar for a mobile screen.
 *
 * 56px rather than the previous 42px, and the title is `text-xl font-semibold`
 * against `text-sm` row labels. Before this the title and the rows beneath it
 * rendered at the same size, so a screen had no typographic hierarchy at all —
 * only two shades of grey.
 */
export function MobileScreenHeader({
  title,
  leading,
  trailing,
  titleClassName,
  subtitle,
}: {
  title: string;
  /** Hamburger or back control. Must be at least 44px square. */
  leading?: ReactNode;
  trailing?: ReactNode;
  /**
   * Overrides the title size. For screens whose title is user content (a chat
   * title, a workflow name) rather than a fixed screen name — those are
   * sentences, and at `text-xl` they truncate to a few words.
   */
  titleClassName?: string;
  subtitle?: ReactNode;
}) {
  return (
    <header className="flex min-h-[56px] shrink-0 items-center gap-1 border-b border-border bg-background px-2">
      {leading}
      <div
        className={cn(
          "min-w-0 flex-1",
          // Without a leading control the title would sit flush against the
          // 8px bar padding, half a step off the 16px content inset every
          // card below it uses.
          leading ? "px-1" : "px-2",
        )}
      >
        <h1
          className={cn(
            "truncate tracking-tight text-foreground",
            titleClassName ?? "text-xl font-semibold",
          )}
        >
          {title}
        </h1>
        {subtitle && (
          <div className="truncate text-xs text-muted-foreground">{subtitle}</div>
        )}
      </div>
      {trailing}
    </header>
  );
}

/**
 * Back control for a drill-in screen.
 *
 * Explicit px, not `h-10 w-10`: rem sizing renders at the root font-size, and
 * at the smallest Appearance step `h-10` measures under 44px — on the only way
 * out of the screen.
 */
export function MobileBackButton({
  onClick,
  label,
}: {
  onClick: () => void;
  label: string;
}) {
  return (
    <button
      type="button"
      onClick={onClick}
      aria-label={label}
      className="flex min-h-[44px] min-w-[44px] items-center justify-center rounded-md text-muted-foreground active:bg-muted"
    >
      <ChevronLeft className="h-5 w-5" />
    </button>
  );
}

/**
 * Scroll region for a grouped-card screen.
 *
 * Owns the page inset and the rhythm between groups so individual screens
 * can't disagree about either. The bottom padding is generous on purpose —
 * the last group otherwise ends flush against the viewport edge with nothing
 * to signal that it is the end rather than a clipped list.
 */
export function MobileScreenBody({
  children,
  className,
}: {
  children: ReactNode;
  className?: string;
}) {
  return (
    <div
      className={cn(
        "min-h-0 flex-1 space-y-6 overflow-y-auto px-4 py-4 pb-10",
        className,
      )}
    >
      {children}
    </div>
  );
}

/**
 * Label above a card group — the small-caps section heading of the
 * iOS-settings idiom. Sits outside the card so the card itself stays a clean
 * stack of equal rows.
 */
export function MobileSectionLabel({ children }: { children: ReactNode }) {
  return (
    <h2 className="mb-2 px-1 text-xs font-semibold uppercase tracking-wider text-muted-foreground">
      {children}
    </h2>
  );
}

/**
 * A group of rows sharing one rounded, raised container.
 *
 * Rows are expected to carry `MOBILE_ROW` (or its own `border-b
 * last:border-b-0`), which is what confines dividers to the space *between*
 * rows inside the group instead of running edge to edge across the screen.
 *
 * `overflow-hidden` clips the first and last row's corners to the container
 * radius; it does not clip the container's own shadow.
 */
export function MobileCardGroup({
  label,
  children,
  className,
}: {
  label?: string;
  children: ReactNode;
  className?: string;
}) {
  return (
    <section>
      {label && <MobileSectionLabel>{label}</MobileSectionLabel>}
      <div
        className={cn(
          "overflow-hidden rounded-lg elevation-1",
          className,
        )}
      >
        {children}
      </div>
    </section>
  );
}

/**
 * Base classes for a row inside a `MobileCardGroup`.
 *
 * 64px, not the 44px floor: every row on this surface carries two lines of
 * text, and at the mobile type scale 44px crowds them.
 *
 * `active:bg-foreground/5` rather than `active:bg-muted` — in dark mode
 * `--muted` *is* the card's own background, so the press state would be
 * invisible on exactly the surface this row lives on.
 */
export const MOBILE_ROW =
  "flex min-h-16 w-full items-center gap-3 border-b border-border px-4 py-3 text-left last:border-b-0 active:bg-foreground/5";

/**
 * Tinted icon chip for a row's leading slot.
 *
 * `bg-primary/10` gives the eye a colored anchor per row without introducing
 * a non-semantic color, and it stays visible on a raised dark card where
 * `bg-muted` would resolve to the card background.
 */
export function MobileRowIcon({ icon: Icon }: { icon: LucideIcon }) {
  return (
    <span className="flex h-9 w-9 shrink-0 items-center justify-center rounded-lg bg-primary/10 text-primary">
      <Icon className="h-4 w-4" />
    </span>
  );
}

/**
 * Full-screen empty state.
 *
 * The previous shape was one line of grey text centered in a void, which reads
 * as a rendering failure rather than a state. An icon, a headline that names
 * the state, a line that says what would fill it, and — where the surface can
 * actually offer one — a primary action.
 */
export function MobileEmptyState({
  icon: Icon,
  title,
  description,
  action,
}: {
  icon: LucideIcon;
  title: string;
  description?: string;
  action?: ReactNode;
}) {
  return (
    <div className="flex flex-1 flex-col items-center justify-center px-8 py-12 text-center">
      <span className="mb-4 flex h-16 w-16 items-center justify-center rounded-full bg-primary/10 text-primary">
        <Icon className="h-7 w-7" />
      </span>
      <p className="text-base font-semibold text-foreground">{title}</p>
      {description && (
        <p className="mt-1.5 max-w-[17rem] text-sm text-muted-foreground">
          {description}
        </p>
      )}
      {action && <div className="mt-6">{action}</div>}
    </div>
  );
}

/**
 * Primary action button sized for a thumb.
 *
 * 48px explicit, for the same root-font-size reason as `MobileBackButton`.
 */
export const MOBILE_PRIMARY_ACTION =
  "flex min-h-[48px] items-center justify-center gap-2 rounded-lg bg-primary px-5 text-sm font-medium text-primary-foreground active:opacity-80 disabled:opacity-60";
