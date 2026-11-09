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
// (deploy/observability/spec/project_gen.json) to the viewer's scope: every
// panel reads only the org's own product_events, narrowed by the environment
// and service scope that forge's sql-panel compiler binds.
//
// It has no code of its own: the test is the contract.
package dashspeccontract
