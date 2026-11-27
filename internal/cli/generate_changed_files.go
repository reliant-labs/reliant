package cli

import (
	"fmt"
	"io"
	"strings"

	"github.com/reliant-labs/forge/internal/checksums"
)

// The closing line counts what the run changed ("64 file(s) written, 5
// changed (2 new, 3 updated)") and printChangedFiles names them under it.
//
// The counts alone left the one question an agent runs generate to answer —
// did MY package's contract_gen.go and mock_gen.go come out regenerated? —
// to a hash comparison: a Ranger agent changed a contract signature, ran
// generate, read "5 changed", and grepped hashes to find out which five. The
// per-step lines above do not answer it either: they announce what each step
// rendered, written or not, and scroll away on a long run.
//
// One path per line, so `| grep mock_gen.go` finds it. Forge's own .forge/
// state moves on most runs that write; it is one count at the end rather than
// paths that push the ones the reader is looking for off the list. The list
// is capped so a first run of a large project (hundreds of new files) does
// not bury what follows it; --verbose names every path.

// changedFilesShown caps the paths the closing summary names without
// --verbose.
const changedFilesShown = 20

// printChangedFiles names, one per line, the paths sum counts as changed:
// new, then updated, then removed, each sorted (WriteSummary's own order).
// verbose names every path, .forge/ state included; otherwise at most
// changedFilesShown are named and the rest counted.
func printChangedFiles(w io.Writer, sum checksums.WriteSummary, verbose bool) {
	type entry struct{ verb, path string }
	var shown []entry
	state, hidden := 0, 0
	for _, set := range []struct {
		verb  string
		paths []string
	}{{"new", sum.Created}, {"updated", sum.Updated}, {"removed", sum.Deleted}} {
		for _, p := range set.paths {
			switch {
			case verbose:
				shown = append(shown, entry{set.verb, p})
			case strings.HasPrefix(p, ".forge/"):
				state++
			case len(shown) < changedFilesShown:
				shown = append(shown, entry{set.verb, p})
			default:
				hidden++
			}
		}
	}
	for _, e := range shown {
		fmt.Fprintf(w, "   %-8s %s\n", e.verb, e.path)
	}
	if hidden > 0 {
		fmt.Fprintf(w, "   … and %d more changed file(s) (--verbose lists every one)\n", hidden)
	}
	if state > 0 {
		fmt.Fprintf(w, "   + %d forge state file(s) under .forge/\n", state)
	}
}
