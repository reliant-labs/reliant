/**
 * RubberBandScroller — Virtuoso Scroller with overscroll containment.
 *
 * Prevents scroll chaining to ancestors. No custom physics.
 */

import React, { forwardRef } from "react";

export interface RubberBandContext {
  footer?: React.ReactNode;
  isStreaming?: boolean;
}

export const RubberBandScroller = forwardRef<
  HTMLDivElement,
  React.ComponentPropsWithRef<"div"> & { context?: RubberBandContext }
>(function RubberBandScroller({ children, style, context: _context, ...props }, ref) {
  return (
    <div ref={ref} style={{ ...style, overscrollBehavior: "contain" }} {...props}>
      {children}
    </div>
  );
});
