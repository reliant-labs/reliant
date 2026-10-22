/**
 * Hamburger trigger for the navigation drawer.
 *
 * A shared component (not copy-pasted per screen) so every top-level screen's
 * button is pixel-identical and opens the same drawer instance via
 * `mobileDrawerStore` — see that store for why the trigger and the drawer
 * itself are decoupled.
 */

import { Menu } from "lucide-react";
import { useMobileDrawerStore } from "../../store/mobileDrawerStore";

export function MobileMenuButton() {
  const open = useMobileDrawerStore((s) => s.open);

  return (
    <button
      type="button"
      onClick={open}
      aria-label="Open menu"
      // Explicit px, not `h-10 w-10`: the root font-size is 14px, so
      // rem-based sizing renders at 87.5% and `h-10` measures 35px — under
      // the 44px minimum, on the only way into every other screen.
      className="flex min-h-[44px] min-w-[44px] items-center justify-center rounded-md text-muted-foreground active:bg-muted"
    >
      <Menu className="h-5 w-5" />
    </button>
  );
}
