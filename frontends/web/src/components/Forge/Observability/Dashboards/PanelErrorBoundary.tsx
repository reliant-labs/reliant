// Copyright (c) 2025 Reliant Labs

/**
 * ONE PANEL CANNOT TAKE DOWN THE DASHBOARD.
 *
 * A dashboard is a grid of user- and forge-authored definitions, each drawn by
 * its own renderer. A definition the renderer did not expect (a panel with no
 * `series`, a kind from a newer forge) used to throw out of the grid and
 * through every boundary up to the app's, which replaced the whole app with
 * "Something went wrong". Each panel now renders inside this boundary: the
 * throw stops at its own cell, which says the panel couldn't be drawn and
 * offers what still works (its definition, Remove, Try again), and every
 * other panel keeps drawing. The definition is shown in place, as JSON, not in
 * the panel drawer: the drawer reads the same definition and could throw too.
 *
 * The boundary has to sit ABOVE everything that reads the panel, including the
 * props the grid computes for it (its menu's hrefs), so the grid hands it a
 * render function (`children`) that is called inside `RenderPanel`, a child of
 * the boundary, rather than an element whose props were already built in the
 * grid's own render.
 */

import { Component, type ErrorInfo, type ReactNode } from "react";
import { AlertTriangle } from "lucide-react";

import Card from "@/components/forge-ui/card";
import { logger } from "@/lib/logger";
import type { Panel } from "@/services/controlPlane/observability/types";

import { spanOf } from "./dashboardModel";
import { BUTTON_SMALL } from "./parts";

export interface PanelErrorBoundaryProps {
  panel: Panel;
  /** Draws the panel. Called inside the boundary, so a throw while building its props is caught too. */
  children: () => ReactNode;
  /** Removes the panel from the dashboard, when the reader may edit it. */
  onRemove?: () => void;
}

interface State {
  error: Error | null;
}

export class PanelErrorBoundary extends Component<PanelErrorBoundaryProps, State> {
  state: State = { error: null };

  static getDerivedStateFromError(error: unknown): State {
    return { error: error instanceof Error ? error : new Error(String(error)) };
  }

  componentDidCatch(error: Error, info: ErrorInfo) {
    logger.error(`Dashboard panel ${this.props.panel.id} failed to render:`, error, info.componentStack);
  }

  componentDidUpdate(previous: PanelErrorBoundaryProps) {
    // A new definition (an edit, a newer version of the dashboard) gets a fresh try.
    if (this.state.error && previous.panel !== this.props.panel) this.setState({ error: null });
  }

  render() {
    if (!this.state.error) return <RenderPanel render={this.props.children} />;
    return <PanelFailed panel={this.props.panel} onRemove={this.props.onRemove} onRetry={() => this.setState({ error: null })} />;
  }
}

function RenderPanel({ render }: { render: () => ReactNode }) {
  return <>{render()}</>;
}

/** The width a definition asks for; a definition too odd to say keeps the full row. */
function safeSpan(panel: Panel): number {
  try {
    return spanOf(panel);
  } catch {
    return 4;
  }
}

/** The definition as the reader would paste it into an issue; never a second throw. */
function specText(panel: Panel): string {
  try {
    return JSON.stringify(panel, null, 2);
  } catch {
    return String(panel);
  }
}

function PanelFailed({ panel, onRemove, onRetry }: { panel: Panel; onRemove?: () => void; onRetry: () => void }) {
  const span = safeSpan(panel);
  const title = typeof panel.title === "string" && panel.title ? panel.title : String(panel.id ?? "Panel");
  return (
    <Card
      padding="none"
      className="relative min-w-0 md:[grid-column:span_var(--span-md)/span_var(--span-md)] xl:[grid-column:span_var(--span)/span_var(--span)]"
      style={{ ["--span" as string]: String(span), ["--span-md" as string]: String(Math.min(span, 2)) }}
      data-testid={`dashboard-panel-failed-${panel.id}`}
    >
      <div role="alert" className="flex items-start gap-2 px-4 py-3">
        <AlertTriangle className="mt-0.5 h-4 w-4 shrink-0 text-warning-ink" aria-hidden="true" />
        <div className="min-w-0 flex-1 space-y-1">
          <h2 className="truncate text-sm font-medium text-ink">{title}</h2>
          <p className="text-xs text-ink-muted">This panel couldn&apos;t be drawn. Its definition may use something this console doesn&apos;t read yet; the rest of the dashboard is unaffected.</p>
          <div className="flex flex-wrap items-center gap-2 pt-1">
            <button type="button" className={BUTTON_SMALL} onClick={onRetry} data-testid={`dashboard-panel-retry-${panel.id}`}>
              Try again
            </button>
            {onRemove && (
              <button type="button" className={BUTTON_SMALL} onClick={onRemove} data-testid={`dashboard-panel-failed-remove-${panel.id}`}>
                Remove
              </button>
            )}
          </div>
          <details className="pt-1 text-xs text-ink-muted" data-testid={`dashboard-panel-failed-spec-${panel.id}`}>
            <summary className="cursor-pointer select-none rounded-sm font-medium text-ink focus:outline-none focus-visible:ring-2 focus-visible:ring-ring">View spec</summary>
            <pre className="mt-1 max-h-48 overflow-auto rounded-md border border-border/60 bg-surface-sunken p-2 font-mono text-xs text-ink">{specText(panel)}</pre>
          </details>
        </div>
      </div>
    </Card>
  );
}
