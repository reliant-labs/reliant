// Copyright (c) 2025 Reliant Labs

/**
 * DEV-ONLY proving harness for the panel editor (PanelDrawer). NOT a product
 * surface.
 *
 * The real drawer, over the fixture app the dashboards preview provides, for
 * every kind the builder offers: pick one in the drawer, or open on it.
 *
 *   /forge-dashboards-preview?page=panel-editor                    a new panel (time series)
 *   …&kind=bar|heatmap|sql|postgres|text|funnel|…                  a new panel of that kind
 *   …&kind=sql&mode=edit                                           an existing sql panel, read on open
 *   …&kind=postgres&mode=edit                                      an existing postgres panel (database app)
 *   …&envs=all                                                     every environment in scope (a postgres panel says why it cannot read)
 *
 * Statements the fixture refuses, to look at a refusal: a postgres statement
 * that is not a read (DELETE FROM users), or one past $3.
 */

import { useState } from "react";
import { useSearch } from "@tanstack/react-router";

import type { Panel } from "@/services/controlPlane/observability/types";

import { FIXTURE_ENVS } from "../../__fixtures__/fixtureApi";
import { PanelDrawer, type DrawerMode } from "../PanelDrawer";
import { starterPanel, VISUALIZATIONS, withDisplay, withKind, type Visualization } from "../panelBuilder";

const dashboard = { id: "checkout-latency", title: "Checkout latency", filters: [], panels: [] };

const EXISTING: Partial<Record<string, Panel>> = {
  sql: withDisplay(
    {
      id: "slow-queries",
      title: "Slow database calls",
      kind: "sql",
      signal: "",
      series: [],
      unit: "ms",
      sql: "SELECT toStartOfInterval(Timestamp, toIntervalSecond({step:UInt32})) AS t,\n       ServiceName AS service,\n       quantile(0.95)(Duration) / 1e6 AS p95\nFROM otel_traces\nWHERE Timestamp >= {from:DateTime64(9)} AND Timestamp < {to:DateTime64(9)}\n  AND SpanAttributes['db.system'] != ''\nGROUP BY t, service\nORDER BY t",
    },
    { style: "area" }
  ),
  postgres: {
    id: "signups",
    title: "Signups per hour",
    kind: "postgres",
    signal: "",
    series: [],
    postgres: {
      database: "app",
      draw: "line",
      time: "t",
      values: ["signups"],
      sql: "SELECT date_trunc('hour', created_at) AS t, count(*) AS signups\nFROM users\nWHERE created_at >= $1 AND created_at < $2\nGROUP BY 1\nORDER BY 1",
    },
  },
};

function panelFor(kind: string | undefined, mode: DrawerMode): Panel {
  const starter = starterPanel(dashboard);
  if (mode !== "add" && kind && EXISTING[kind]) return EXISTING[kind]!;
  if (!kind || !VISUALIZATIONS.some((v) => v.kind === kind)) return starter;
  const panel = withKind(starter, kind as Visualization);
  if (panel.kind === "postgres") return { ...panel, postgres: { ...panel.postgres!, database: "app" } };
  return panel;
}

export default function PanelEditorPreview() {
  const search = useSearch({ strict: false }) as { kind?: string; mode?: string; envs?: string };
  const mode: DrawerMode = search.mode === "edit" || search.mode === "view" ? search.mode : "add";
  const [saved, setSaved] = useState<Panel | null>(null);
  const [open, setOpen] = useState(true);
  const envIds = search.envs === "all" ? FIXTURE_ENVS.map((e) => e.id) : ["env-prod"];
  return (
    <div className="space-y-3 p-6" data-testid="panel-editor-preview">
      <p className="text-sm text-ink-muted">The panel editor over fixtures. Save prints the panel forge would store.</p>
      {!open && (
        <button type="button" className="rounded-md border border-border px-3 py-1.5 text-sm text-ink" onClick={() => setOpen(true)}>
          Open the editor again
        </button>
      )}
      {saved && <pre className="max-h-96 overflow-auto rounded-md border border-border/60 bg-surface-sunken p-3 font-mono text-xs text-ink">{JSON.stringify(saved, null, 2)}</pre>}
      {open && (
        <PanelDrawer
          key={`${search.kind ?? ""}-${mode}-${search.envs ?? ""}`}
          mode={mode}
          panel={panelFor(search.kind, mode)}
          dashboardTitle={dashboard.title}
          scope={{ orgId: "org-fixture", projectId: "shop", envIds, filters: [] }}
          range="1h"
          services={[]}
          environments={FIXTURE_ENVS}
          env={search.envs === "all" ? "all" : "prod"}
          scopeLabel={search.envs === "all" ? "every environment" : "prod"}
          version={4}
          envScope="prod"
          saving={false}
          error={null}
          onCancel={() => setOpen(false)}
          onSave={(panel) => {
            setSaved(panel);
            setOpen(false);
          }}
        />
      )}
    </div>
  );
}
