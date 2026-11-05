// Copyright (c) 2025 Reliant Labs

// Package dashspeccontract holds the web console's dashboard panel builder to
// forge's dashboard spec. The builder (frontends/web, Dashboards/panelBuilder)
// writes every panel it can make to a fixture exactly as it sends them; this
// package's test parses that fixture with forge's ParseDashboard, the
// validator control-plane runs on every dashboard save, at the forge this
// module pins. A panel the builder makes that forge would refuse fails here,
// before it can fail a save.
//
// It has no code of its own: the test is the contract.
package dashspeccontract
