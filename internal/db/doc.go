// Package db provides database models, migrations, and repository implementations
// for reliant's persistent storage layer.
//
// The DB layer is hand-owned (the Repository, per-aggregate interfaces and
// sqlc-generated queries under internal/db/postgres/generated) until the forge
// ORM replaces it (P5b, research/forge-migration/PLAN.md). The ORM projects
// into internal/store (forge.yaml database.orm.package), not here, so this
// package's names (Chat, Message, Repo, …) and the generated entities live
// side by side while aggregates port one lane at a time. The porting rules
// are in SCHEMA.md.
package db
