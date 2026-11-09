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
//
// # Adding to the vocabulary: readers first
//
// ParseDashboard is strict: a panel kind it does not know (`"browse" is not
// one of: ...`) and a field it does not know (`unknown field "link"`) are
// problems, so a typo is never a panel that silently draws nothing. The cost
// is that a reader pinned to an older forge REFUSES a dashboard using a newer
// kind or field, rather than skipping it. So a new kind or field (KindBrowse,
// Panel.Browse, Panel.Link) rolls out readers first:
//
//  1. forge releases the vocabulary;
//  2. every reader that validates (the control plane's dashboard service, a
//     release bundle's reader) pins that forge;
//  3. only then does anything write it: the console, an author, a generator.
//
// The dashboards forge generates must not use a kind or field in the release
// that introduces it (none uses browse or link), or a reader on the previous
// pin would refuse the bundle carrying them. Promotion
// (PromoteDashboardToCode) carries a saved dashboard from the control plane
// into the repository, the reader that already accepted it, never the other
// way. Once validated, the HyperDX renderer needs no change: it skips every
// IsConsoleKind panel and ignores the console-only fields.
package dashspec
