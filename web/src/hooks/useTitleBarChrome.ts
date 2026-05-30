import { useEffect, useState, type CSSProperties } from "react";

/**
 * Shared chrome logic for the three Electron-aware app headers
 * (`Layout/Header`, `Settings/SettingsHeader`, `workflow/WorkflowHeader`).
 *
 * Owns:
 *   - Platform detection (Electron? macOS?)
 *   - Fullscreen subscription (initial status + Electron `onFullscreenChanged`
 *     + a `resize` listener — the latter catches macOS Split View / Fill modes
 *     that don't emit `onFullscreenChanged`. All three callers previously did
 *     the same thing inline; the older `useFullscreen` hook in this folder is
 *     missing the resize listener and is currently unused.)
 *   - macOS traffic-light padding (80px when Electron + macOS + not fullscreen)
 *   - Drag-region style objects for the bar / non-drag children
 *
 * Does NOT own header JSX, button handlers, theme, or unrelated state.
 *
 * @param options.alignedToWindowEdge  Defaults to true. When false, the caller
 *   sits inside a content area (e.g. right of a vertical nav), so the traffic
 *   lights aren't behind it and no extra padding is needed. Only `Layout/Header`
 *   exercises this — the other two headers always span the window edge.
 * @param options.collapsedPadding  CSS padding-left applied when traffic-light
 *   spacing isn't needed. Defaults to `"12px"` to match `Layout/Header`; the
 *   other headers pass `"8px"`.
 */
export interface UseTitleBarChromeOptions {
  alignedToWindowEdge?: boolean;
  collapsedPadding?: string;
}

export interface UseTitleBarChromeResult {
  isElectron: boolean;
  isMac: boolean;
  isFullscreen: boolean;
  /** Padding to apply to the bar's leading container to clear traffic lights. */
  trafficLightPadding: string;
  /** Apply to the header bar and any draggable filler regions. */
  dragRegionStyle: CSSProperties;
  /** Apply to interactive children that must remain clickable. */
  noDragRegionStyle: CSSProperties;
  /** True when the app should render its own min/max/close buttons (non-Mac Electron). */
  showWindowControls: boolean;
}

export function useTitleBarChrome(
  options: UseTitleBarChromeOptions = {},
): UseTitleBarChromeResult {
  const { alignedToWindowEdge = true, collapsedPadding = "12px" } = options;

  const isElectron = Boolean(window.electronAPI);
  const isMac = window.electronAPI?.platform === "darwin";
  const [isFullscreen, setIsFullscreen] = useState(false);

  useEffect(() => {
    if (!window.electronAPI) return;

    let cancelled = false;

    const checkFullscreenStatus = async () => {
      if (!window.electronAPI?.getFullscreenStatus) return;
      const isFS = await window.electronAPI.getFullscreenStatus();
      if (!cancelled) setIsFullscreen(isFS);
    };

    void checkFullscreenStatus();
    window.addEventListener("resize", checkFullscreenStatus);

    let unsubscribe: (() => void) | undefined;
    if (window.electronAPI.onFullscreenChanged) {
      unsubscribe = window.electronAPI.onFullscreenChanged((fs: boolean) => {
        if (!cancelled) setIsFullscreen(fs);
      });
    }

    return () => {
      cancelled = true;
      window.removeEventListener("resize", checkFullscreenStatus);
      if (unsubscribe) unsubscribe();
    };
  }, []);

  const trafficLightPadding =
    !isFullscreen && isMac && alignedToWindowEdge ? "80px" : collapsedPadding;

  const dragRegionStyle: CSSProperties = isElectron
    ? ({
        WebkitAppRegion: "drag",
        WebkitUserSelect: "none",
        userSelect: "none",
      } as CSSProperties)
    : {};

  const noDragRegionStyle: CSSProperties = isElectron
    ? ({ WebkitAppRegion: "no-drag" } as CSSProperties)
    : {};

  const showWindowControls = isElectron && !isMac;

  return {
    isElectron,
    isMac,
    isFullscreen,
    trafficLightPadding,
    dragRegionStyle,
    noDragRegionStyle,
    showWindowControls,
  };
}
