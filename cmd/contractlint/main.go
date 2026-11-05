package main

import (
	"os"
	"strings"

	"golang.org/x/tools/go/analysis/multichecker"

	"github.com/reliant-labs/forge/internal/linter/contract"
)

func main() {
	// Parse a top-level -exclude flag out of os.Args before handing control to
	// multichecker.Main (which would otherwise treat unknown top-level flags
	// as fatal). The exclude list mirrors forge.yaml's contracts.exclude and
	// lets users invoke contractlint directly:
	//
	//   contractlint -exclude=internal/naming,internal/assets ./...
	//
	// The same -exclude flag is also registered on each analyzer (so it's
	// reachable via -contract.exclude=..., -requirecontract.exclude=..., etc.,
	// which is how `go vet -vettool=contractlint -contract.exclude=...` works).
	excludes, remaining := extractExcludeFlag(os.Args[1:])
	if len(excludes) > 0 {
		contract.SetExcludes(excludes)
	}
	os.Args = append([]string{os.Args[0]}, remaining...)

	multichecker.Main(
		contract.Analyzer,
		contract.RequireContractAnalyzer,
		contract.ExportedVarsAnalyzer,
	)
}

// extractExcludeFlag pulls a top-level -exclude / --exclude flag (with either
// `-exclude=val` or `-exclude val` form) out of the argument list and returns
// the parsed value plus the remaining args. It only consumes the FIRST
// matching occurrence; subsequent uses are left in place so the per-analyzer
// flag handler (or multichecker error reporting) can deal with them.
func extractExcludeFlag(args []string) ([]string, []string) {
	var excludes []string
	out := make([]string, 0, len(args))

	for i := 0; i < len(args); i++ {
		arg := args[i]
		// Stop scanning at "--" — anything after is positional.
		if arg == "--" {
			out = append(out, args[i:]...)
			break
		}

		name, value, hasValue := parseFlag(arg)
		if name != "exclude" {
			out = append(out, arg)
			continue
		}

		if hasValue {
			excludes = append(excludes, splitExcludes(value)...)
			continue
		}
		// `-exclude val` form — consume the next arg as the value.
		if i+1 < len(args) {
			excludes = append(excludes, splitExcludes(args[i+1])...)
			i++
			continue
		}
		// `-exclude` with no value — treat as empty (clears the list).
	}

	return excludes, out
}

// parseFlag returns (name, value, hasValue) for a CLI argument. Returns an
// empty name for non-flag args.
func parseFlag(arg string) (string, string, bool) {
	if !strings.HasPrefix(arg, "-") || arg == "-" {
		return "", "", false
	}
	trimmed := strings.TrimLeft(arg, "-")
	if trimmed == "" {
		return "", "", false
	}
	if eq := strings.IndexByte(trimmed, '='); eq >= 0 {
		return trimmed[:eq], trimmed[eq+1:], true
	}
	return trimmed, "", false
}

func splitExcludes(value string) []string {
	if value == "" {
		return nil
	}
	parts := strings.Split(value, ",")
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		p = strings.TrimSpace(p)
		if p != "" {
			out = append(out, p)
		}
	}
	return out
}
