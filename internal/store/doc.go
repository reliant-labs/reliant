// Package store holds reliant's queries over the forge ORM projection of the
// applied schema; internal/db is the Repository facade over it.
//
// # Lock order
//
// A transaction that takes the user's org lock (LockUserOrgs) takes it first,
// before any row lock (FOR UPDATE, FOR NO KEY UPDATE, FOR SHARE), and never
// after one. The org backfill (AssignUnassignedOrg) follows the same order and
// takes its rows SKIP LOCKED. Every write whose correctness depends on an org
// decision takes the lock (org_lock.go); internal/db's
// TestOrgSensitiveWritesTakeTheOrgLock holds them to it.
package store
