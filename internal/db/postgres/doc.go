// This is the persistence layer: the exported surface is concrete data types
// and their store methods, consumed through the interfaces the calling
// services declare for themselves (the narrow-consumer-interface pattern, as
// in internal/runs/contract.go). A contract.go here would be one wide
// interface over every query, which no caller consumes.
//
// It lives in its own file so it survives the P5b lanes deleting the stores
// one aggregate at a time.
//
//forge:lint-disable-next-line forge-exclude-contract-outbound-io: sqlc store layer, replaced by the forge ORM in P5b (research/forge-migration/PLAN.md)
//forge:exclude-contract: postgres implementations of db/core's store interfaces over sqlc-generated queries
package postgres
