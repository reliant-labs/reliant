// Copyright (c) 2025 Reliant Labs

/**
 * The forge console's EXIT, as a quiet icon in the sidebar's brand row (the
 * project switcher beside it is Console/ConsoleProjectSwitcher).
 *
 * X in Electron, a back arrow on the web, and Esc everywhere (bound in
 * ForgeLayout). It navigates to the logical parent route rather than
 * history.back() — see lib/routeParent.ts.
 */

import { ArrowLeft, X } from "lucide-react";

import { Tooltip } from "@/components/ui/Tooltip";
import { useTitleBarChrome } from "@/hooks/useTitleBarChrome";

/** The exit, as an icon in the sidebar's brand row. */
export function ForgeCloseButton({ onClose }: { onClose: () => void }) {
  const { isElectron, noDragRegionStyle } = useTitleBarChrome({ alignedToWindowEdge: false });
  const label = isElectron ? "Close Apps" : "Back to app";
  return (
    <Tooltip content={`${label} (Esc)`} placement="bottom" delay={300}>
      <button
        type="button"
        onClick={onClose}
        style={noDragRegionStyle}
        className="inline-flex h-7 w-7 shrink-0 items-center justify-center rounded-md text-ink-muted transition-colors hover:bg-ink/5 hover:text-ink focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-accent"
        aria-label={label}
        data-testid="forge-close"
      >
        {isElectron ? <X className="h-4 w-4" /> : <ArrowLeft className="h-4 w-4" />}
      </button>
    </Tooltip>
  );
}
