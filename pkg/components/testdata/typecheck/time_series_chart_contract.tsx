// The TimeSeriesChart's public type contract, checked by `tsc` in
// TestComponentTypeContracts (library_type_contract_test.go). Every
// `@ts-expect-error` below is a misuse the type must REJECT.
import React from "react";

import TimeSeriesChart, { type TimeSeries, type TimeSeriesAnnotation } from "./time_series_chart";

const series: TimeSeries[] = [
  {
    id: "visits",
    label: "Visits",
    variant: "area",
    points: [
      { t: "2026-11-01", value: 120 },
      { t: new Date(2026, 10, 2), value: null },
      { t: Date.UTC(2026, 10, 3), value: 340 },
    ],
  },
  {
    id: "signups",
    label: "Signups",
    color: "#10b981",
    points: [{ t: "2026-11-01T12:00:00Z", value: 4 }],
  },
];

const annotations: TimeSeriesAnnotation[] = [
  { t: "2026-11-02", label: "Posted on HN", color: "#ff6600" },
];

export function Accepted() {
  return (
    <>
      <TimeSeriesChart ariaLabel="Visits" series={series} />
      <TimeSeriesChart
        ariaLabel="Visits and signups"
        series={series}
        annotations={annotations}
        variant="area"
        height={320}
        formatValue={(v) => v.toFixed(0)}
        formatTime={(d) => d.toISOString()}
        yDomain={[0, 1000]}
        showLegend={false}
        emptyMessage={<span>Nothing yet</span>}
      />
    </>
  );
}

export function Rejected() {
  return (
    <>
      {/* @ts-expect-error a chart needs an accessible name */}
      <TimeSeriesChart series={series} />
      {/* @ts-expect-error there is no "bar" variant */}
      <TimeSeriesChart ariaLabel="x" series={series} variant="bar" />
      {/* @ts-expect-error an annotation needs a label */}
      <TimeSeriesChart ariaLabel="x" series={series} annotations={[{ t: "2026-11-02" }]} />
      <TimeSeriesChart
        ariaLabel="x"
        // @ts-expect-error a value is a number or null
        series={[{ id: "a", label: "A", points: [{ t: 1, value: "3" }] }]}
      />
    </>
  );
}
