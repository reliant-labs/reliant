// Copyright (c) 2025 Reliant Labs

// Package dashspeccontract holds the web console's dashboard panel builder to
// forge's dashboard spec. The builder (frontends/web, Dashboards/panelBuilder)
// writes every panel it can make to a fixture exactly as it sends them; this
// package's test parses that fixture with forge's ParseDashboard, the
// validator control-plane runs on every dashboard save, at the forge this
// module pins. A panel the builder makes that forge would refuse fails here,
// before it can fail a save.
//
// It also holds the generated product-usage dashboard this repository ships
// (deploy/observability/spec/project_gen.json) to what forge's sql-panel
// compiler binds: every panel reads only the unqualified product_events, so
// the database the hosted executor runs it in (the org's own; the compiler
// refuses the server's) and that executor's grants are the isolation boundary.
// The environment and service predicates are filters inside that boundary: a
// restricted viewer's environments always narrow the rows, while an empty
// list (an unrestricted viewer, no service filter) matches every row of the
// org's own data.
//
// It has no code of its own: the test is the contract.
package dashspeccontract
