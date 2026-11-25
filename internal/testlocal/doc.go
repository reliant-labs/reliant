// Package testlocal reads the local test record, .forge/tests/runs/, for
// `forge test report`, `coverage`, `runs` and `ingest`.
//
// It turns run directories into the inputs of pkg/testreport's documents:
// the lanes' parsed results, their coverage merged across lanes (refused
// across trees, builds or source versions), each covered file's repo path and
// git blob id, and the diff's changed lines. The numbers are pkg/testreport's
// (the same locally and hosted); this package only finds and reads what they
// are computed from.
//
// Git is reached only through the [Git] seam, so everything here is testable
// on run directories written into a temp dir with a fake git.
//
//forge:exclude-contract: a reader of on-disk records plus pure functions over what it read; the exported methods are accessors on those records (Run, Lane, Assembly), and the one seam callers substitute is the Git func, so there is no service for a contract to describe
package testlocal
