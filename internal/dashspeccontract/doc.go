// Copyright (c) 2025 Reliant Labs

// Package dashspeccontract holds the web console's dashboard panel builder to
// forge's dashboard spec. The builder (frontends/web, Dashboards/panelBuilder)
// writes every panel it can make to a fixture exactly as it sends them; this
// package's test parses that fixture with forge's ParseDashboard, the
// validator control-plane runs on every dashboard save, at the forge this
// module pins. A panel the builder makes that forge would refuse fails here,
// before it can fail a save.
//
// It also holds forge's built-in analytics dashboards this repository ships
// (deploy/observability/spec/project_gen.json: the Overview's analytics
// panels, Users, Product and Acquisition) to what forge's sql-panel compiler
// binds: every statement reads only the unqualified tenant tables, so the
// database the hosted executor runs it in (the org's own; the compiler
// refuses the server's) and that executor's grants are the isolation boundary.
// The environment predicate is a filter inside that boundary: a restricted
// viewer's environments always narrow the rows, while an empty list (an
// unrestricted viewer) matches every row of the org's own data.
//
// It has no code of its own: the test is the contract.
package dashspeccontract
