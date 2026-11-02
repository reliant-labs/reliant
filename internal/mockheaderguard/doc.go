// Package mockheaderguard proves that the generated mock-data header
// describes a layout forge actually creates.
//
// The header of every generated `src/mocks/<entity>.ts` tells a reader where
// its values come from. It used to say they mirror `db/fixtures` — a
// directory `forge project new` has never created — so a reader chasing the
// provenance of a fixture found nothing. The values in fact come from the
// project's own seed plan (codegen.SeedProjection over seedplan.Plan), whose
// on-disk surface is `db/seeds/`.
//
// The guard lives in its own package, rather than beside the generator,
// because it asserts an agreement BETWEEN two packages: the template in
// internal/templates and the directory set internal/generator writes. It
// scaffolds a real project and checks every `db/<dir>` the header names
// against that project's filesystem, so the obligation is derived from what
// forge does rather than from a list of forbidden strings.
//
// There is no runtime code here on purpose — the guard must never become
// something production code can call or disable.
package mockheaderguard
