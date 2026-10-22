// Package db is reliant's database component: the concrete persistence API
// (*Repo), its transactions (RunTx and the ctx-carried tx bridge), the pools
// and advisory locks, the migrator, the core aliases, the credential sealing
// around the stores and the test harness.
//
// Every statement the Repo runs is in internal/store, the forge ORM's package
// (forge.yaml database.orm.package): the generated entities and stores plus
// the hand _repo_ext.go queries. The ORM projects there, not here, so this
// package's names (Chat, Message, Repo, …) and the generated entities live
// side by side. What stays here is behaviour: guards, multi-statement
// transactions, update-hub emission and sealing.
//
// The Repo is concrete, and this package declares no interface over it.
// Declare the role you need where you need it: a consumer lists the methods
// it calls in its own package (deps.go, contract.go, or beside the function
// that takes it), and *Repo satisfies it structurally. Composition code —
// internal/app, the activity and tool registries — takes *Repo itself.
// The query rules are in SCHEMA.md.
package db
