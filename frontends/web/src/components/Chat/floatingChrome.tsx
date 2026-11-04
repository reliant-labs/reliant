/**
 * Floating chrome for the mobile chat screen (dev-docs/design/mobile-chat-redesign.md
 * §4.3).
 *
 * The top bar and the composer band float OVER the transcript instead of
 * taking rows in the flex column. The transcript scroller runs the full
 * height and reserves the bars' measured heights as content padding, so the
 * first and last messages can still scroll fully clear of them, while
 * everything in between scrolls under translucent glass.
 *
 * `FloatingChromeFrame` is the screen's root. It owns two measurements — the
 * top bar and the bottom band (status slot + approvals + composer) — taken by
 * ResizeObserver from the elements registered through `useFloatingChrome()`,
 * and publishes them twice:
 *
 * - as the custom properties `--chat-top-inset` / `--chat-bottom-inset` on
 *   the frame, for CSS that only needs to clear the bars (the pinned prompt,
 *   the scroll-to-bottom button, sticky toolbars, the catch-up overlay);
 * - as numbers through `useFloatingChromeInsets()`, for the transcript's
 *   virtualizer, which has to know the padding to compute "the end" and to
 *   align jumps below the bar.
 *
 * The scroll edge — whether content is scrolled under the top bar, or sits
 * under the composer — is written straight onto the frame as
 * `data-scroll-edge-top` / `data-scroll-edge-bottom`, and the bars draw their
 * hairline from those through `group-data-*` variants. It changes on scroll,
 * so it never goes through React state: a re-render per scroll event would
 * undo the virtualizer's direct DOM updates.
 *
 * Outside a frame (the desktop shell, embeds) nothing floats: the hooks return
 * null / zero insets and every consumer keeps its docked layout.
 */

import {
  createContext,
  useCallback,
  useContext,
  useLayoutEffect,
  useMemo,
  useRef,
  useState,
  type CSSProperties,
  type ReactNode,
} from "react";
import { cn } from "../../lib/utils";

export interface FloatingChromeInsets {
  /** The top bar's height, px. */
  top: number;
  /** The bottom band's height (status slot, approvals, composer, safe area), px. */
  bottom: number;
}

export interface ScrollEdges {
  /** Content is scrolled under the top bar. */
  top: boolean;
  /** Content continues under the composer band. */
  bottom: boolean;
}

interface FloatingChromeControl {
  /** Callback ref for the top bar. */
  topBarRef: (element: HTMLElement | null) => void;
  /** Callback ref for the bottom band. */
  bottomBandRef: (element: HTMLElement | null) => void;
  /** Called by the transcript as it scrolls; only changes touch the DOM. */
  reportScrollEdges: (edges: ScrollEdges) => void;
}

const NO_INSETS: FloatingChromeInsets = { top: 0, bottom: 0 };

// Two contexts so the control (stable for the frame's lifetime) never
// re-renders its consumers when a bar changes height. ChatPresenter consumes
// the control only; the transcript consumes both.
const ControlContext = createContext<FloatingChromeControl | null>(null);
const InsetsContext = createContext<FloatingChromeInsets>(NO_INSETS);

/** The frame's controls, or null when the chrome is docked (desktop). */
export function useFloatingChrome(): FloatingChromeControl | null {
  return useContext(ControlContext);
}

/** The bars' measured heights; zero when docked. */
export function useFloatingChromeInsets(): FloatingChromeInsets {
  return useContext(InsetsContext);
}

/**
 * The glass both bars (and the pinned prompt strip) are made of. Opaque under
 * reduced transparency: a blur is exactly what that preference asks to drop.
 */
export const FLOATING_BAR_SURFACE =
  "bg-background/85 backdrop-blur-xl reduce-transparency:bg-background reduce-transparency:backdrop-blur-none";

/**
 * The top bar's bottom hairline: none at rest, shown while content is
 * scrolled under the bar — and always under reduced transparency, where the
 * opaque bar would otherwise meet the transcript with no edge at all.
 */
export const FLOATING_TOP_EDGE =
  "border-b border-transparent transition-colors duration-150 group-data-[scroll-edge-top]/chat-chrome:border-border/60 reduce-transparency:border-border/60";

/** The bottom band's top hairline, by the same rule against the composer. */
export const FLOATING_BOTTOM_EDGE =
  "border-t border-transparent transition-colors duration-150 group-data-[scroll-edge-bottom]/chat-chrome:border-border/60 reduce-transparency:border-border/60";

/** Observe an element's border-box height; 0 once it is gone. */
function useObservedHeight(element: HTMLElement | null): number {
  const [height, setHeight] = useState(0);
  useLayoutEffect(() => {
    if (!element) {
      setHeight(0);
      return;
    }
    // Whole pixels: the virtualizer re-lays out on every change, and a
    // subpixel wobble from a transform or zoom is not a new geometry.
    const measure = () => setHeight(Math.round(element.getBoundingClientRect().height));
    measure();
    if (typeof ResizeObserver === "undefined") return;
    const observer = new ResizeObserver(measure);
    observer.observe(element);
    return () => observer.disconnect();
  }, [element]);
  return height;
}

export function FloatingChromeFrame({
  children,
  className,
}: {
  children: ReactNode;
  className?: string;
}) {
  const frameRef = useRef<HTMLDivElement>(null);
  const [topBar, setTopBar] = useState<HTMLElement | null>(null);
  const [bottomBand, setBottomBand] = useState<HTMLElement | null>(null);
  const top = useObservedHeight(topBar);
  const bottom = useObservedHeight(bottomBand);

  const edgesRef = useRef<ScrollEdges>({ top: false, bottom: false });
  const reportScrollEdges = useCallback((edges: ScrollEdges) => {
    const frame = frameRef.current;
    const current = edgesRef.current;
    if (!frame || (current.top === edges.top && current.bottom === edges.bottom)) return;
    edgesRef.current = edges;
    frame.toggleAttribute("data-scroll-edge-top", edges.top);
    frame.toggleAttribute("data-scroll-edge-bottom", edges.bottom);
  }, []);

  const control = useMemo<FloatingChromeControl>(
    () => ({ topBarRef: setTopBar, bottomBandRef: setBottomBand, reportScrollEdges }),
    [reportScrollEdges],
  );
  const insets = useMemo(() => ({ top, bottom }), [top, bottom]);

  // `children` is a stable element from the screen's render, so an inset
  // change re-renders this frame and the inset consumers, not the screen.
  return (
    <ControlContext.Provider value={control}>
      <InsetsContext.Provider value={insets}>
        <div
          ref={frameRef}
          data-floating-chrome=""
          className={cn("group/chat-chrome relative", className)}
          style={
            {
              "--chat-top-inset": `${top}px`,
              "--chat-bottom-inset": `${bottom}px`,
            } as CSSProperties
          }
        >
          {children}
        </div>
      </InsetsContext.Provider>
    </ControlContext.Provider>
  );
}
