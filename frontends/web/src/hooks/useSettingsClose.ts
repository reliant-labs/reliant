import { useCallback } from "react";
import { useLocation, useNavigate } from "@tanstack/react-router";
import { getParentRouteNavigateOptions } from "../lib/routeParent";

/**
 * Returns a stable callback that closes the settings page and returns the
 * user to the logical parent route (per src/lib/routeParent.ts). We
 * deliberately do NOT call router.history.back(): if the user arrived via a
 * non-app referrer in the same tab, back() leaves the app entirely.
 */
export function useSettingsClose(): () => void {
  const navigate = useNavigate();
  const { pathname } = useLocation();
  return useCallback(() => {
    navigate(getParentRouteNavigateOptions(pathname));
  }, [navigate, pathname]);
}
