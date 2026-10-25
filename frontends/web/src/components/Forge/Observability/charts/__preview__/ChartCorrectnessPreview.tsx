// Copyright (c) 2025 Reliant Labs

import { UPlotChart, type ChartMarker } from "../UPlotChart";

/** Real uPlot, deterministic edge cases at the dashboard's minimum chart height. */
export default function ChartCorrectnessPreview() {
  const start = Date.UTC(2026, 9, 25) / 1000;
  const markers: ChartMarker[] = Array.from({ length: 12 }, (_, i) => ({
    timeSec: start + i * 10,
    label: `v2026.10.25-release-${i}`,
    description: `Production promoted release ${i}`,
  }));
  const bands = markers.map((m, i) => ({ startSec: m.timeSec, endSec: m.timeSec + 10, label: `RPCErrorRateHigh-${i}` }));
  return (
    <section className="flex flex-wrap items-start gap-4 p-4" data-testid="chart-correctness-preview">
      <div className="w-80 min-w-0 border border-border bg-card p-2">
        <h2 className="text-sm text-foreground">DAU · Oct 25, 2026</h2>
        <UPlotChart label="October singleton" timestamps={[start]} series={[{ label: "DAU", values: [7] }]} height={120} legend={false} />
      </div>
      <div className="w-60 min-w-0 border border-border bg-card p-2">
        <h2 className="text-sm text-foreground">Sparse RPC observations</h2>
        <UPlotChart label="Sparse observations" timestamps={[start, start + 60, start + 120]} series={[{ label: "RPCs", values: [7, null, 9] }]} height={120} legend={false} />
      </div>
      <div className="w-60 min-w-0 border border-border bg-card p-2">
        <h2 className="text-sm text-foreground">RPC · crowded annotations</h2>
        <UPlotChart label="Crowded annotations" timestamps={[start, start + 60, start + 120]} series={[{ label: "RPCs", values: [7, 8, 9] }]} markers={markers} bands={bands} height={120} legend={false} />
      </div>
    </section>
  );
}
