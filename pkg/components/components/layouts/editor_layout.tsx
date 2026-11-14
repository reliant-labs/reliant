import React from "react";

interface EditorLayoutProps {
  /** Top toolbar — menu, file actions, view controls. */
  toolbar: React.ReactNode;
  /** Left panel — typically tools, layers, or component palette. */
  leftPanel?: React.ReactNode;
  /** Main canvas / editor surface. */
  canvas: React.ReactNode;
  /** Right panel — typically properties, inspector, or context info. */
  rightPanel?: React.ReactNode;
  /** Bottom panel — typically a console, terminal, or timeline. Optional. */
  bottomPanel?: React.ReactNode;
  /** Left panel width (px). Default 240. */
  leftWidth?: number;
  /** Right panel width (px). Default 280. */
  rightWidth?: number;
  /** Bottom panel height (px). Default 200. */
  bottomHeight?: number;
}

/**
 * Three-pane editor shell (Figma / VS Code / Notion-block pattern).
 *
 * All four side regions are slots so the parent owns content. The bottom
 * panel is optional and only renders if provided. Panels do not collapse
 * — wire up your own toggle if needed.
 *
 * Panels have fixed pixel widths; the canvas takes the remainder.
 */
export default function EditorLayout({
  toolbar,
  leftPanel,
  canvas,
  rightPanel,
  bottomPanel,
  leftWidth = 240,
  rightWidth = 280,
  bottomHeight = 200,
}: EditorLayoutProps) {
  return (
    <div className="flex h-screen w-full flex-col bg-surface-muted">
      <div className="flex shrink-0 items-center gap-2 border-b border-border bg-surface px-4 py-2">
        {toolbar}
      </div>

      <div className="flex min-h-0 flex-1">
        {leftPanel && (
          <aside
            aria-label="Tools"
            className="shrink-0 relative overflow-y-auto border-r border-border bg-surface"
            style={{ width: leftWidth }}
          >
            {leftPanel}
          </aside>
        )}

        <div className="flex min-w-0 flex-1 flex-col">
          <main aria-label="Canvas" className="min-h-0 flex-1 relative overflow-auto">
            {canvas}
          </main>

          {bottomPanel && (
            <aside
              aria-label="Bottom panel"
              className="shrink-0 relative overflow-y-auto border-t border-border bg-surface"
              style={{ height: bottomHeight }}
            >
              {bottomPanel}
            </aside>
          )}
        </div>

        {rightPanel && (
          <aside
            aria-label="Properties"
            className="shrink-0 relative overflow-y-auto border-l border-border bg-surface"
            style={{ width: rightWidth }}
          >
            {rightPanel}
          </aside>
        )}
      </div>
    </div>
  );
}
