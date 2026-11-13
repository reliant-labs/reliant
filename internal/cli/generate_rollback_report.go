package cli

import (
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

// Root-cause attribution for the failed-generate report.
//
// FRICTION (third dogfood run): a generate run aborted on a typecheck
// error in a generated test helper (`invalid composite literal type
// tagging.Role`). The rollback rewound 71 files — correctly — but that
// restored an OLDER handlers_crud_ops_gen.go, and the errors the user was
// then shown were:
//
//	internal/handlers/documents/scoping.go:66:10: s.crudGetDocumentOp undefined
//
// four times, against a hand-written file that compiles standalone. The
// user spent four turns debugging a correct file. The real fault was two
// files away and had already been detected — forge simply did not say
// which error was the one that mattered.
//
// The rollback itself is right and stays. Two things about how it REPORTS
// are not:
//
//  1. Downstream errors produced AFTER a rollback restored stale files
//     were presented as equals to the root cause. An error citing a file
//     forge never wrote this run is an artifact of the revert, and the
//     user must never be sent to debug it.
//
//  2. The build verdict on the restored tree was conflated with the
//     FIDELITY of the revert. Both claims were collapsed into one
//     sentence, so a non-compiling tree suppressed "back to its pre-run
//     state" — as though the rollback had failed to restore it.
//
// (2) was over-corrected and is now stated as two independent facts.
// The revert IS byte-faithful: it rewrites every journaled path from the
// bytes captured before the run, so "back to its pre-run state" is true
// whenever the journal restored cleanly, whatever the tree then does
// under a compiler. Whether that tree BUILDS is a separate question, and
// a "no" is usually a statement about where the user already was, not
// about the rollback.
//
// That distinction is load-bearing, because a pre-run tree that does not
// compile is a supported, deliberate state. `forge scaffold worker x
// --no-generate` exists to stage a component without running the
// pipeline — it writes cmd/<bin>/cmd/workers/x.go referencing an accessor
// that only a later generate emits. Every project mid-stage is in exactly
// this state, and telling those users the restored tree "does NOT
// compile" as if the revert had stranded them sends them to debug a
// rollback that did its job perfectly. Forge now reports the revert's
// fidelity unconditionally, and the compile verdict beside it, attributed
// to the pre-run tree where it belongs.

// rollbackRootCauseMarker labels the error that actually aborted the run.
// It is the one error on screen the user should act on, and it is printed
// under this marker whether or not anything else follows.
const rollbackRootCauseMarker = "ROOT CAUSE (this is the error to fix)"

// rollbackArtifactMarker labels errors that appeared only AFTER the
// rollback restored older files. They describe the reverted tree, not the
// user's code.
const rollbackArtifactMarker = "⚠️  The errors below are ARTIFACTS OF THE ROLLBACK, not your bug"

// rollbackConsistency is the verdict on the tree the rollback restored:
// did it actually compile? Checked=false means no verdict was reachable
// (no Go module, no toolchain), which is reported as silence rather than
// as either claim.
type rollbackConsistency struct {
	Checked bool
	Builds  bool
	Output  string
	// SkippedNoGoChange is set when no build ran because the run changed
	// no Go build input (goBuildInputsTouched): the compiler would only
	// have re-judged the tree the user started with.
	SkippedNoGoChange bool
}

// restoredTreeCompileCheck is the post-rollback compile check; a test seam
// over goBuildRestoredTree.
var restoredTreeCompileCheck = goBuildRestoredTree

// goBuildInputsTouched reports whether a failed run changed anything the Go
// compiler reads: a journaled path the run changed (changed, read before the
// revert), or a path outside the journal that still differs from before the
// run (the residue). Nothing it reports is ever assumed: when the residue
// could not be checked, a path outside the journal may have changed
// unseen, so the answer is yes.
//
// Embedded files (//go:embed) are not inputs here. Their bytes never decide
// whether a package compiles, and the revert puts every journaled file back
// where it was, so a pattern's match set is the pre-run one.
func goBuildInputsTouched(changed []string, residue rollbackResidue) bool {
	if !residue.Checked {
		return true
	}
	for _, p := range append(append([]string(nil), changed...), residue.Changed...) {
		if isGoBuildInput(p) {
			return true
		}
	}
	return false
}

// isGoBuildInput reports whether a project-relative path is one `go build`
// reads to decide whether the tree compiles: Go and cgo/assembly sources,
// the module files, and anything vendored.
func isGoBuildInput(rel string) bool {
	rel = filepath.ToSlash(rel)
	if strings.HasPrefix(rel, "vendor/") {
		return true
	}
	switch path.Base(rel) {
	case "go.mod", "go.sum", "go.work", "go.work.sum":
		return true
	}
	switch path.Ext(rel) {
	case ".go", ".s", ".S", ".sx", ".c", ".h", ".cc", ".cpp", ".cxx", ".hh", ".hpp", ".hxx",
		".m", ".f", ".F", ".for", ".f90", ".syso", ".swig", ".swigcxx":
		return true
	}
	return false
}

// rollbackReport is everything the failure report needs, gathered before
// any of it is printed so the whole message can be ordered by usefulness
// rather than by the order the pipeline happened to learn things.
type rollbackReport struct {
	Restored    []string
	Preserved   []string
	StepErr     error
	Consistency rollbackConsistency
	// Residue is the verdict on the rest of the tree: whether anything the
	// revert did not own also differs from before the run. The "back to
	// its pre-run state" claim is made only when it is verified Clean.
	Residue rollbackResidue
	// Unborn are the scaffold-once files this run created and the revert
	// removed again, with their birth records. Each will be scaffolded
	// afresh by the next run, so when one of them is what broke this run
	// the user needs the way to decline it, here, rather than in a skill.
	Unborn []string
}

// goBuildRestoredTree runs `go build ./...` against the tree the rollback
// just restored, to check whether the "back to your pre-run state"
// reassurance is actually true before forge offers it.
//
// Why verify rather than roll back more cleverly: a fully-consistent
// rollback would have to revert files this run did NOT write (compose.go
// was kept from an EARLIER successful run, so it is outside the journal by
// construction), which means either snapshotting the whole working tree —
// explicitly out of scope for the journal, and guaranteed to manufacture
// spurious diffs from `go mod tidy` and friends — or reverting files the
// user may have hand-edited. Both are worse than the honest report. So
// forge keeps the bounded revert and stops CLAIMING more than it verified.
//
// A build failure here is not an error condition for the pipeline: the run
// has already failed, and this only decides which message is truthful.
// Anything that prevents a verdict returns Checked=false.
//
// Like the validate step it compiles and links nothing (compileTree): the
// verdict is `go build ./...`'s, without the link of every main package that
// made this check take minutes on a large project.
func goBuildRestoredTree(projectDir string) rollbackConsistency {
	if _, err := os.Stat(filepath.Join(projectDir, "go.mod")); err != nil {
		return rollbackConsistency{}
	}
	if _, err := exec.LookPath("go"); err != nil {
		return rollbackConsistency{}
	}
	rep, err := compileTree(projectDir, false)
	if err != nil {
		out, buildErr := goBuildFallback(projectDir, nil)
		if buildErr == nil {
			return rollbackConsistency{Checked: true, Builds: true}
		}
		return rollbackConsistency{Checked: true, Builds: false, Output: out}
	}
	if rep.Build == "" {
		return rollbackConsistency{Checked: true, Builds: true}
	}
	return rollbackConsistency{Checked: true, Builds: false, Output: rep.Build}
}

// classifyPostRollbackErrors splits post-rollback compiler output into the
// lines that cite a file forge did NOT write this run, and everything
// else.
//
// The first bucket is the dangerous one and the reason this function
// exists. A file forge never touched is byte-identical to what the user
// had before the run, so an error citing it is guaranteed to be a
// consequence of some OTHER file having been reverted — scoping.go's four
// `crudGetDocumentOp undefined` errors were exactly this. Those files are
// correct on disk, and the report says so.
//
// Only lines carrying file:line coordinates are classified; `# package`
// headers and continuation lines carry no path to judge and fall through
// to the second bucket rather than being silently dropped.
func classifyPostRollbackErrors(output string, touched map[string]bool) (untouched, other []string) {
	for _, line := range strings.Split(strings.TrimRight(output, "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		rel := errorLineFile(line)
		if rel != "" && !touched[rel] {
			untouched = append(untouched, line)
			continue
		}
		other = append(other, line)
	}
	return untouched, other
}

// errorLineFile extracts the project-relative path from a compiler error
// line ("internal/x/y.go:66:10: msg"). Returns "" when the line carries no
// file:line coordinate, which is the signal to leave it unclassified.
func errorLineFile(line string) string {
	trimmed := strings.TrimSpace(line)
	if strings.HasPrefix(trimmed, "#") {
		return ""
	}
	idx := strings.Index(trimmed, ".go:")
	if idx == -1 {
		return ""
	}
	path := trimmed[:idx+len(".go")]
	// Guard against a message that merely mentions a .go file after the
	// real coordinate; the path is the leading token.
	if strings.ContainsAny(path, " \t") {
		return ""
	}
	return filepath.ToSlash(path)
}

// writeRollbackReport renders the whole failure report in order of
// usefulness: what to fix, what was reverted, and only then the
// post-rollback noise — explicitly labelled as noise.
//
// Split from rollbackGeneratedTree (which owns the side effects) so the
// message itself is unit-testable without a real tree, a real journal, or
// a real failed build.
func writeRollbackReport(w io.Writer, rep rollbackReport) {
	// 1. The root cause, FIRST and unmistakable. This is the line whose
	//    absence cost four turns.
	fmt.Fprintf(w, "\n❌ %s:\n", rollbackRootCauseMarker)
	fmt.Fprintf(w, "   %s\n", rootCauseSummary(rep.StepErr))
	if out := rootCauseOutput(rep.StepErr); out != "" {
		for _, line := range limitLines(out, compilerOutputTailLines) {
			fmt.Fprintf(w, "   %s\n", line)
		}
	}

	// 2. What the rollback did. INDEPENDENT facts, reported separately:
	//    the revert put every listed file back to its exact pre-run bytes;
	//    whether the REST of the tree is also as it was (verified against
	//    the pre-run fingerprint, never assumed); and whether the tree
	//    compiles. Conflating them is what made this report lie twice — see
	//    the note above, and generate_rollback_verify.go.
	switch {
	case rep.Residue.Clean():
		fmt.Fprintf(w, "\n↩️  generate failed its own validation — reverted %d file(s) this run wrote; your tree is back to its pre-run state (checked against the pre-run tree; no `git checkout` needed%s):\n",
			len(rep.Restored), compileVerdictSuffix(rep.Consistency))
	case rep.Residue.Checked:
		fmt.Fprintf(w, "\n↩️  generate failed its own validation — reverted %d file(s) this run wrote, but the tree is NOT fully back to its pre-run state (see below%s):\n",
			len(rep.Restored), compileVerdictSuffix(rep.Consistency))
	default:
		fmt.Fprintf(w, "\n↩️  generate failed its own validation — reverted %d file(s) this run wrote to their pre-run bytes%s. The rest of the tree could not be checked — run `git status` to confirm it is as you left it:\n",
			len(rep.Restored), compileVerdictSuffix(rep.Consistency))
	}
	for _, p := range rep.Restored {
		fmt.Fprintf(w, "   - %s\n", p)
	}
	if rep.Residue.Checked && len(rep.Residue.Changed) > 0 {
		fmt.Fprintf(w, "\n   ⚠️  %d file(s) still differ from before the run:\n", len(rep.Residue.Changed))
		writeResidue(w, rep.Residue.Changed)
	}
	if rep.Consistency.Checked && !rep.Consistency.Builds {
		writePreRunDoesNotCompileNote(w)
	}
	writeUnbornScaffolds(w, rep.Unborn)

	// 3. The post-rollback errors, quarantined behind the artifact marker.
	if rep.Consistency.Checked && !rep.Consistency.Builds && strings.TrimSpace(rep.Consistency.Output) != "" {
		writeArtifactSection(w, rep)
	}
}

// writeUnbornScaffolds names the scaffold-once files the revert un-created,
// and how to decline one. A scaffold whose presence breaks the run it is
// born in (reliant's serve.go, which switched composition on) is otherwise
// re-born and rolled back by every run, and deleting it is no remedy: the
// birth record that would make the deletion stick went with the revert.
func writeUnbornScaffolds(w io.Writer, unborn []string) {
	if len(unborn) == 0 {
		return
	}
	fmt.Fprintf(w, "\n   🌱 %d scaffold-once file(s) this run created were removed again, so the next run scaffolds them afresh:\n", len(unborn))
	for _, p := range unborn {
		fmt.Fprintf(w, "      - %s\n", p)
	}
	fmt.Fprintf(w, "      If this project should not have one, decline it — the decline survives a failed run:\n")
	fmt.Fprintf(w, "        %s project disown %s --absent --reason \"<why>\"\n", Name(), strings.Join(unborn, " "))
}

// compileVerdictSuffix adds the "and it compiles" reassurance ONLY when a
// build actually proved it. Silence when unchecked or when the tree does
// not build: the non-compiling case gets the fuller note below, and an
// unchecked tree has earned neither claim.
func compileVerdictSuffix(c rollbackConsistency) string {
	switch {
	case c.Checked && c.Builds:
		return ", verified: it compiles"
	case c.SkippedNoGoChange:
		return "; the run changed no Go file, so there was nothing to recompile"
	}
	return ""
}

// writePreRunDoesNotCompileNote states the build verdict WITHOUT implying
// the rollback caused it.
//
// The distinction is the whole fix. A restored tree that does not compile
// is, far more often than not, a pre-run tree that did not compile — and
// the revert is what proves it, because the revert is byte-faithful. The
// staged-scaffold workflow reaches this state on purpose: `forge scaffold
// worker x --no-generate` writes cmd/<bin>/cmd/workers/x.go, which
// references an accessor that only exists after a generate. Between those
// two commands the tree does not build, by design, and a generate that
// refuses in between must not tell the user their tree was damaged.
func writePreRunDoesNotCompileNote(w io.Writer) {
	fmt.Fprintf(w, "\n   ⚠️  That tree does NOT compile on its own — and it did not before this run\n")
	fmt.Fprintf(w, "   either. The revert restored every file above byte for byte, so this is the\n")
	fmt.Fprintf(w, "   state you started in, not damage the rollback caused.\n")
	fmt.Fprintf(w, "   The usual reason is work STAGED but not yet generated (`forge scaffold ...\n")
	fmt.Fprintf(w, "   --no-generate` writes a component whose accessor appears only once generate\n")
	fmt.Fprintf(w, "   runs). Fix the ROOT CAUSE above and re-run `forge generate`: that emits the\n")
	fmt.Fprintf(w, "   missing halves and the tree builds.\n")
}

// writeArtifactSection prints the post-rollback build errors under the
// artifact marker, leading with the ones that cite files forge never
// wrote — the class that sent a user to debug a correct file.
func writeArtifactSection(w io.Writer, rep rollbackReport) {
	touched := map[string]bool{}
	for _, p := range rep.Restored {
		touched[filepath.ToSlash(p)] = true
	}
	untouched, other := classifyPostRollbackErrors(rep.Consistency.Output, touched)

	fmt.Fprintf(w, "\n%s:\n", rollbackArtifactMarker)
	if len(untouched) > 0 {
		files := citedFiles(untouched)
		fmt.Fprintf(w, "   forge did not write %s this run, so %s unchanged on disk and\n",
			pluralFiles(len(files)), pluralIs(len(files)))
		fmt.Fprintf(w, "   correct. %s reported only because a file %s was reverted.\n",
			pluralThey(len(files)), pluralDependsOn(len(files)))
		fmt.Fprintf(w, "   Do not debug %s — fix the ROOT CAUSE above instead.\n", pluralThem(len(files)))
		for _, f := range files {
			fmt.Fprintf(w, "     · %s\n", f)
		}
		for _, line := range limitLines(strings.Join(untouched, "\n"), compilerOutputTailLines) {
			fmt.Fprintf(w, "   %s\n", line)
		}
	}
	if len(other) > 0 {
		fmt.Fprintf(w, "   Other errors against the reverted tree (also post-rollback, also not the root cause):\n")
		for _, line := range limitLines(strings.Join(other, "\n"), compilerOutputTailLines) {
			fmt.Fprintf(w, "   %s\n", line)
		}
	}
}

// citedFiles returns the sorted unique file paths named by a set of
// compiler error lines — the four identical scoping.go errors name one
// file, and the report should say one file.
func citedFiles(lines []string) []string {
	seen := map[string]bool{}
	var files []string
	for _, line := range lines {
		if f := errorLineFile(line); f != "" && !seen[f] {
			seen[f] = true
			files = append(files, f)
		}
	}
	sort.Strings(files)
	return files
}

// rootCauseSummary is the failing step's user-facing message, stripped of
// the wrapping the pipeline adds.
func rootCauseSummary(err error) string {
	if err == nil {
		return "generate failed"
	}
	return err.Error()
}

// rootCauseOutput returns the compiler/typecheck output carried by the
// aborting error, when it carries any.
func rootCauseOutput(err error) string {
	var ve *validateBuildError
	if !errors.As(err, &ve) {
		return ""
	}
	return strings.TrimRight(ve.Output, "\n")
}

// limitLines caps a block of output at n lines, appending an elision note
// naming how much was cut.
func limitLines(output string, n int) []string {
	lines := strings.Split(strings.TrimRight(output, "\n"), "\n")
	if len(lines) <= n {
		return lines
	}
	out := append([]string{}, lines[:n]...)
	return append(out, fmt.Sprintf("… (%d more line(s); full output in %s/%s)",
		len(lines)-n, failedGenerateDir, failedGenerateErrorFile))
}

func pluralFiles(n int) string {
	if n == 1 {
		return "this file"
	}
	return "these files"
}

func pluralIs(n int) string {
	if n == 1 {
		return "it is"
	}
	return "they are"
}

func pluralThey(n int) string {
	if n == 1 {
		return "It is"
	}
	return "They are"
}

func pluralDependsOn(n int) string {
	if n == 1 {
		return "it depends on"
	}
	return "they depend on"
}

func pluralThem(n int) string {
	if n == 1 {
		return "it"
	}
	return "them"
}
