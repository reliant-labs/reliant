/**
 * Proving harness for the forge → reliant token bridge (the @theme block in
 * src/index.css). NOT a product surface — this exists so the bridge can be
 * verified against REAL forge components rather than against a synthetic
 * swatch grid, in every color scheme and both light and dark.
 *
 * The components below are installed verbatim by `forge component install`
 * and are never edited; that is the whole point of mapping at the theme
 * layer. If a token is unmapped it shows up here as a transparent fill or
 * inherited text, which is what a missing mapping looks like in the UI.
 *
 * Everything is wrapped in `.forge-ui`, which is what gives forge's `accent`
 * its own meaning (reliant's primary action color) without disturbing the
 * ~159 existing `bg-accent` call sites elsewhere in the app.
 */
import { useState } from "react";

import Badge from "./badge";
import DataTable from "./data_table";
import EmptyState from "./empty_state";
import StatGrid from "./stat_grid";
import StatusDot from "./status_dot";

const SCHEMES = [
  "professional-blue",
  "refined-neutral",
  "modern-teal",
  "slate",
  "forest",
  "vibrant-pink",
  "energetic-orange",
  "bold-red",
  "purple-classic",
  "pure-black",
] as const;

type Row = Record<string, unknown> & {
  service: string;
  env: string;
  status: string;
  replicas: string;
};

const ROWS: Row[] = [
  { service: "admin-server", env: "prod", status: "Running", replicas: "3/3" },
  { service: "daemon-gateway", env: "prod", status: "Degraded", replicas: "1/3" },
  { service: "reliant-api-server", env: "staging", status: "Running", replicas: "2/2" },
  { service: "temporal-worker", env: "staging", status: "Failed", replicas: "0/2" },
];

export default function ForgeTokenSandbox() {
  const [scheme, setScheme] = useState<string>(
    () => document.documentElement.getAttribute("data-color-scheme") ?? "professional-blue",
  );
  const [dark, setDark] = useState<boolean>(
    () => document.documentElement.classList.contains("dark"),
  );

  function applyScheme(next: string) {
    setScheme(next);
    document.documentElement.setAttribute("data-color-scheme", next);
  }

  function applyDark(next: boolean) {
    setDark(next);
    document.documentElement.classList.toggle("dark", next);
  }

  return (
    <div className="min-h-screen overflow-auto bg-background p-8">
      <div className="mb-6 flex flex-wrap items-center gap-3">
        <label className="text-sm text-foreground" htmlFor="forge-scheme">
          Color scheme
        </label>
        <select
          id="forge-scheme"
          data-testid="scheme-select"
          value={scheme}
          onChange={(e) => applyScheme(e.target.value)}
          className="rounded-md border border-border bg-card px-2 py-1 text-sm text-foreground"
        >
          {SCHEMES.map((s) => (
            <option key={s} value={s}>
              {s}
            </option>
          ))}
        </select>
        <button
          type="button"
          data-testid="toggle-dark"
          onClick={() => applyDark(!dark)}
          className="rounded-md border border-border bg-card px-3 py-1 text-sm text-foreground"
        >
          {dark ? "Dark" : "Light"}
        </button>
      </div>

      {/* Everything forge renders lives under .forge-ui. */}
      <div className="forge-ui space-y-8" data-testid="forge-scope">
        <StatGrid
          columns={4}
          stats={[
            { value: "14", label: "Workloads", trend: { direction: "up", value: "+2" } },
            { value: "3", label: "Environments", trend: { direction: "flat", value: "0" } },
            { value: "1", label: "Degraded", trend: { direction: "down", value: "-1" } },
            { value: "98.2%", label: "Availability", trend: { direction: "up", value: "+0.3%" } },
          ]}
        />

        <div className="flex flex-wrap items-center gap-3">
          <Badge variant="success" label="Running" />
          <Badge variant="warning" label="Pending" />
          <Badge variant="danger" label="Failed" />
          <Badge variant="info" label="Queued" />
          <Badge variant="neutral" label="Unknown" />
          <StatusDot variant="active" label="healthy" />
          <StatusDot variant="warning" label="degraded" />
          <StatusDot variant="error" label="down" />
        </div>

        <DataTable<Row>
          columns={[
            { key: "service", header: "Service", sortable: true },
            { key: "env", header: "Environment" },
            { key: "status", header: "Status" },
            { key: "replicas", header: "Replicas" },
          ]}
          data={ROWS}
          selectable
          page={1}
          pageSize={10}
          totalItems={ROWS.length}
        />

        <DataTable<Row>
          columns={[
            { key: "service", header: "Service" },
            { key: "env", header: "Environment" },
          ]}
          data={[]}
          loading
        />

        <EmptyState
          title="No environments yet"
          description="Deploy a service to see it here."
          actionLabel="Deploy"
          onAction={() => undefined}
        />
      </div>
    </div>
  );
}
