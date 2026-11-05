// Package dashspec is forge's renderer-neutral dashboard vocabulary
// (forge.dashboards/v1) and its validator: the types a dashboard file is
// written in, and ParseDashboard, which judges one with every problem located
// by line, column and path.
//
// forge generates dashboards in this vocabulary (deploy/observability/), a
// release bundle carries them, and the Reliant control plane saves and promotes
// them. Every one of those reads the same rules from here, so a dashboard forge
// writes is one every reader accepts, and a new panel kind reaches all of them
// with one forge pin bump instead of one hand-kept copy per reader.
//
// Fields are OpenTelemetry names keyed by signal (see CheckField); nothing here
// names a ClickHouse column, a HyperDX field or a chart library.
package dashspec
