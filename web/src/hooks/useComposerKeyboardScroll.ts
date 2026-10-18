import { useEffect, useRef } from "react";
import { useMobileViewport } from "./useMobileViewport";

/** Keep the latest turn visible while the software keyboard resizes the chat. */
export function useComposerKeyboardScroll(enabled: boolean, scrollToBottom: () => void) {
  const viewport = useMobileViewport(enabled);
  const scrollRef = useRef(scrollToBottom);
  scrollRef.current = scrollToBottom;
  useEffect(() => {
    if (!enabled || !viewport.keyboardVisible ||
        !document.activeElement?.closest("[data-mobile-composer]")) return;
    // Wait for the shell resize and virtualized row measurements to settle.
    // Re-schedule during keyboard animation; do not subscribe to transcript
    // scrolling, so users can scroll back through history with it still open.
    let secondFrame = 0;
    const firstFrame = requestAnimationFrame(() => {
      secondFrame = requestAnimationFrame(() => scrollRef.current());
    });
    return () => {
      cancelAnimationFrame(firstFrame);
      cancelAnimationFrame(secondFrame);
    };
  }, [enabled, viewport.keyboardVisible, viewport.height, viewport.width]);
}
