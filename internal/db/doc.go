// Package db provides database models, migrations, and repository implementations
// for reliant's persistent storage layer.
//
// The Repository is hand-owned; every statement it runs is in internal/store,
// the forge ORM's package (forge.yaml database.orm.package): the generated
// entities and stores plus the hand _repo_ext.go queries. The ORM projects
// there, not here, so this package's names (Chat, Message, Repo, …) and the
// generated entities live side by side. This package keeps the Repository
// facade, its transactions (RunTx), the core aliases, the migrations and the
// credential sealing around the stores; splitting the Repository per
// aggregate is P6 (research/forge-migration/PLAN.md). The query rules are in
// SCHEMA.md.
package db
