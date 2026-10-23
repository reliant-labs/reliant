/**
 * Hamburger trigger for the navigation drawer.
 *
 * A shared component (not copy-pasted per screen) so every top-level screen's
 * button is pixel-identical and opens the same drawer instance via
 * `mobileDrawerStore` — see that store for why the trigger and the drawer
 * itself are decoupled.
 *
 * It carries the Inbox's badge as a dot. The Inbox lives behind this button,
 * and an approval or a question waiting on the user is exactly what a phone
 * is opened to deal with: without a mark here it would be invisible until the
 * drawer happened to be opened.
 */

import { Menu } from "lucide-react";
import { useMobileDrawerStore } from "../../store/mobileDrawerStore";
import { cn } from "../../lib/utils";

export function MobileMenuButton() {
  const open = useMobileDrawerStore((s) => s.open);
  const { count, informational, announcement } = useMobileDrawerStore((s) => s.inboxBadge);
  const marked = count > 0 || informational;

  return (
    <button
      type="button"
      onClick={open}
      aria-label={announcement ? `Open menu, ${announcement}` : "Open menu"}
      // Explicit px, not `h-10 w-10`: the root font-size is 14px, so
      // rem-based sizing renders at 87.5% and `h-10` measures 35px — under
      // the 44px minimum, on the only way into every other screen.
      className="relative flex min-h-[44px] min-w-[44px] items-center justify-center rounded-md text-muted-foreground active:bg-muted"
    >
      <Menu className="h-5 w-5" />
      {marked && (
        <span
          data-testid="menu-inbox-dot"
          aria-hidden="true"
          className={cn(
            "absolute right-2 top-2 h-2.5 w-2.5 rounded-full ring-2 ring-background",
            // The same rule as the badge: a blocked run is the primary colour, an
            // automation failure alone is the destructive one.
            count > 0 ? "bg-primary" : "bg-destructive",
          )}
        />
      )}
    </button>
  );
}
