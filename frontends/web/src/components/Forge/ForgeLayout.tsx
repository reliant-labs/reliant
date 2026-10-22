// Copyright (c) 2025 Reliant Labs

/**
 * The pathless layout route over every /forge URL: the console
 * (/forge/p/$project/…, Console/ConsoleLayout) and the entries that resolve
 * into it (/forge and the retired URLs, Console/ForgeResolve).
 *
 * It owns the EXIT. The forge routes sit under the bare `_authenticated`
 * layout, which renders no app chrome, so without this there was no Escape
 * handler on any forge screen and a reader could only leave by editing the
 * URL. Escape navigates to the logical parent route (lib/routeParent.ts),
 * matching SettingsPage; the console's brand row carries the same exit as a
 * button.
 *
 * Project resolution used to live here too. It moved: the console is
 * addressed by the forge project in its path, so ConsoleLayout looks the
 * Reliant project up from that, and only the entries without one need the
 * old ladder (ForgeResolve).
 */

import { useCallback, useEffect } from "react";
import { Outlet, useLocation, useNavigate } from "@tanstack/react-router";

import { hasOpenEscapeLayer } from "@/hooks/useEscapeLayer";
import { getParentRouteNavigateOptions } from "@/lib/routeParent";

export function ForgeLayout() {
  const navigate = useNavigate();
  const { pathname } = useLocation();

  const onClose = useCallback(() => {
    void navigate(getParentRouteNavigateOptions(pathname));
  }, [navigate, pathname]);

  // Capture-phase, and it steps aside for text entry so it cannot eat an
  // Escape meant for a field.
  useEffect(() => {
    const handleKeyDown = (event: KeyboardEvent) => {
      if (event.key !== "Escape") return;
      const target = event.target as HTMLElement | null;
      if (target?.tagName === "INPUT" || target?.tagName === "TEXTAREA" || target?.contentEditable === "true") {
        return;
      }
      // A dialog, popover or menu on top owns Escape: it closes first, alone.
      if (hasOpenEscapeLayer()) return;
      event.preventDefault();
      event.stopPropagation();
      onClose();
    };
    window.addEventListener("keydown", handleKeyDown, true);
    return () => window.removeEventListener("keydown", handleKeyDown, true);
  }, [onClose]);

  return <Outlet />;
}
