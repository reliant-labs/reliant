/**
 * Where the workflow viewer points its camera.
 *
 * ReactFlow's fitView frames EVERYTHING, at whatever zoom that takes. A
 * workflow with an expanded loop is wide, and the viewer usually lives in a
 * 400px side panel, so fit-all opened Get It Right at 14% — node labels a
 * couple of pixels tall and no way to tell which step was running. Readable
 * is the constraint and "everything" is the nice-to-have, so these clamp the
 * zoom to a floor where labels can be read and, when the graph does not fit
 * at that zoom, anchor its START (top-left) in view rather than its middle.
 */

export interface Rect {
  x: number
  y: number
  width: number
  height: number
}

export interface Size {
  width: number
  height: number
}

export interface Viewport {
  x: number
  y: number
  zoom: number
}

/** Node labels are text-xs (12px); below ~0.6 they stop being legible. */
export const OVERVIEW_MIN_ZOOM = 0.6
/** The step a run is on is the thing being watched: give it a larger floor. */
export const FOCUS_MIN_ZOOM = 0.75
/** Never blow a small workflow up past its natural size. */
export const MAX_AUTO_ZOOM = 1
/** Screen-space margin kept around whatever is framed. */
export const FRAME_PADDING_PX = 32

interface FrameOptions {
  minZoom: number
  maxZoom?: number
  paddingPx?: number
}

/**
 * The viewport that frames `bounds` in a pane of `pane` size: as much of it as
 * fits at no less than `minZoom`, centred on any axis where it fits and
 * anchored at its top/left edge on any axis where it does not.
 */
export function frameBounds(bounds: Rect, pane: Size, options: FrameOptions): Viewport {
  const padding = options.paddingPx ?? FRAME_PADDING_PX
  const maxZoom = options.maxZoom ?? MAX_AUTO_ZOOM
  const usableWidth = Math.max(pane.width - padding * 2, 1)
  const usableHeight = Math.max(pane.height - padding * 2, 1)
  const fitZoom = Math.min(
    usableWidth / Math.max(bounds.width, 1),
    usableHeight / Math.max(bounds.height, 1),
  )
  const zoom = Math.min(Math.max(fitZoom, options.minZoom), maxZoom)

  const axis = (start: number, length: number, paneLength: number, usable: number) => {
    const scaled = length * zoom
    return scaled <= usable
      ? (paneLength - scaled) / 2 - start * zoom
      : padding - start * zoom
  }

  return {
    x: axis(bounds.x, bounds.width, pane.width, usableWidth),
    y: axis(bounds.y, bounds.height, pane.height, usableHeight),
    zoom,
  }
}

/** Whether `bounds` is entirely on screen under `viewport`. */
export function isInView(bounds: Rect, viewport: Viewport, pane: Size): boolean {
  const left = -viewport.x / viewport.zoom
  const top = -viewport.y / viewport.zoom
  const right = left + pane.width / viewport.zoom
  const bottom = top + pane.height / viewport.zoom
  return (
    bounds.x >= left &&
    bounds.y >= top &&
    bounds.x + bounds.width <= right &&
    bounds.y + bounds.height <= bottom
  )
}

/** The smallest rect containing every rect given. */
export function unionRects(rects: Rect[]): Rect | null {
  if (rects.length === 0) return null
  let minX = Infinity
  let minY = Infinity
  let maxX = -Infinity
  let maxY = -Infinity
  for (const rect of rects) {
    minX = Math.min(minX, rect.x)
    minY = Math.min(minY, rect.y)
    maxX = Math.max(maxX, rect.x + rect.width)
    maxY = Math.max(maxY, rect.y + rect.height)
  }
  return { x: minX, y: minY, width: maxX - minX, height: maxY - minY }
}

interface FocusCandidate {
  id: string
  type?: string
  data?: { executionStatus?: string }
}

/**
 * The nodes a running workflow should be framed on: the running steps
 * themselves. A running loop group is a container, so it is only the focus
 * when nothing inside any group is known to be running yet.
 */
export function runningFocusNodeIds(nodes: FocusCandidate[]): string[] {
  const running = nodes.filter((node) => node.data?.executionStatus === 'running')
  const steps = running.filter((node) => node.type !== 'expandedLoopNode')
  return (steps.length > 0 ? steps : running).map((node) => node.id).sort()
}
