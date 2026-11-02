/** Geometry only: never use visual (zoomed/keyboard) width to choose a route. */
export interface VisualViewportGeometry {
  height: number;
  offsetTop: number;
  scale: number;
}

/**
 * Browser chrome can consume ~110px. This is an occlusion heuristic, not a
 * keyboard API: floating keyboards and resize-content keyboards may not be
 * detectable. Do not infer occlusion from a previous orientation's height.
 */
export function measureVisualViewport(viewport: VisualViewportGeometry, layoutHeight: number) {
  return {
    height: Math.round(viewport.height),
    offsetTop: Math.round(viewport.offsetTop),
    // Zoom in OR out is not keyboard occlusion. Leave zoom/panning to the UA.
    keyboardOpen: Math.abs(viewport.scale - 1) <= 0.01 && layoutHeight - viewport.height > 150,
  };
}
