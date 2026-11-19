"use client";

// Part of @reliantlabs/forge-web-runtime — the web twin of forge/pkg.
//
// The React end of an interaction (interaction.ts, which is framework-free).
import { useEffect } from "react";

import type { Interaction, InteractionEndAttributes } from "./interaction.js";

/**
 * End `op` after the paint that follows the first commit in which `ready` is
 * true — the screen the user asked for is on it. Pass the component's own
 * readiness (data loaded, overlay gone):
 *
 *   useInteractionEnd(openOp, transcriptLoaded);
 *
 * `attributes` are read from the render in which `ready` became true. A null
 * `op`, or one already ended (superseded, timed out), is ignored.
 */
export function useInteractionEnd(
  op: Interaction | null | undefined,
  ready: boolean,
  attributes?: InteractionEndAttributes,
): void {
  useEffect(() => {
    if (ready && op && !op.ended) {
      op.endAfterPaint(attributes);
    }
    // attributes are deliberately not a dependency: the first ready commit
    // ends the interaction, and a later render cannot change how it ended.
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [op, ready]);
}
