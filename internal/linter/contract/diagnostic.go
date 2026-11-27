package contract

import (
	"fmt"
	"go/token"

	"golang.org/x/tools/go/analysis"
)

// FixHint is the remediation for a method outside its contract. It is also
// the in-process lane's fallback hint.
const FixHint = "either declare the exported method in the contract interface, or unexport it (lowercase) if it's helper-only"

// reportWithHint leaves Message alone for the in-process lane, which already
// renders a separate fix_hint. The Go analysis drivers used by contractlint
// (including go vet -vettool) print related information, so the human fix is
// visible there too. It is not a SuggestedFix: there is no single safe edit.
func reportWithHint(pass *analysis.Pass, pos token.Pos, hint, format string, args ...any) {
	pass.Report(analysis.Diagnostic{
		Pos:     pos,
		Message: fmt.Sprintf(format, args...),
		Related: []analysis.RelatedInformation{{Pos: pos, Message: "Fix: " + hint}},
	})
}
