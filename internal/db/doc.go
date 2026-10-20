// Package db provides database models, migrations, and repository implementations
// for reliant's persistent storage layer.
//
// The DB layer is hand-owned (the Repository, per-aggregate interfaces and
// sqlc-generated queries under internal/db/postgres/generated) until the forge
// ORM replaces it (P5b, research/forge-migration/PLAN.md).
//
//forge:no-orm: the DB layer is a hand-owned Postgres repository over sqlc, not a generated ORM over the schema; forge's ORM now projects every applied table and would redeclare Chat, Message, Project, Daemon and the rest of the types declared here
package db
