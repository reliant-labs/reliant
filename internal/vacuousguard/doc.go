// Package vacuousguard is the repository-wide proof that a green test actually
// ran something.
//
// A failing test is loud. A test that quietly declines to test is silent, and
// silence reads as success on every dashboard forge has. Two shapes produce it,
// and both have already cost this codebase real time:
//
//   - TestProjectWorkflows_* (sibling repo, reliant) skipped itself when a
//     fixture DIRECTORY was missing. A checkout leaves that directory present
//     but EMPTY, so the skip never fired, the loop over its contents ran zero
//     times, and six tests sat hard-red and unwatched. Both halves of that bug
//     are guarded here: a guard that skips instead of failing, and a loop that
//     passes because it never executed.
//
//   - forge's own tree still carries skips conditioned on the presence of files
//     that are tracked in this repository. Those skips cannot fire today, which
//     sounds harmless — it means they are unfalsifiable. Rename the file they
//     stat and the tests do not fail, they evaporate.
//
// THE RULES
//
//	vacuous-loop  A test discovers a set from the filesystem (os.ReadDir,
//	              filepath.Glob, …), ranges over it, and nothing in the test
//	              proves the set was non-empty. Such a test passes when the
//	              discovery returns nothing, which is precisely when something
//	              is wrong.
//
//	dead-skip     A t.Skip that is unconditional (a test that can never run),
//	              or one conditioned on the presence of a REPO-RELATIVE path.
//	              A file tracked in this repository is present on every
//	              checkout, so a skip guarding it either never fires — an
//	              assertion nobody can falsify — or fires because the repo
//	              layout moved, which must be a failure, not a shrug.
//
//	unwritten-gate  An audit category or lint check gates on a generated Go
//	              file in the user's project, and nothing in forge writes that
//	              file. The check then returns its PASSING verdict every time,
//	              on every project, forever. `forge project audit` shipped a
//	              wire_coverage category and two lint flags reading
//	              pkg/app/wire_gen.go for months after the DI rewrite stopped
//	              emitting it; all three were green, and green is what people
//	              read. Every one of them had passing unit tests — the tests
//	              fed the scanner synthetic strings, and none asked whether a
//	              real project could produce the input. The discriminator is
//	              not "is it tested" but "does anything write the file", so the
//	              ledger in gates_test.go answers that per path, with the
//	              emitter named.
//
// # WHAT IS DELIBERATELY NOT A RULE
//
// A skip on a missing TOOLCHAIN (kcl, node, dlv, golangci-lint), on
// testing.Short(), on runtime.GOOS, on an environment opt-in, or on
// os.UserHomeDir() is legitimate: the thing it guards is genuinely absent on
// some machines and no checkout can supply it. Those are left alone, on
// purpose. A rule broad enough to catch them is a rule that gets switched off.
//
// The analysis is a package library rather than a bare test so its rules can be
// unit-tested against planted defects in testdata/. Nothing in forge's
// production path imports it.
package vacuousguard
