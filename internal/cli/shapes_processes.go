package cli

import (
	"encoding/json"
	"fmt"
	"io"
	"regexp"
	"strings"

	"github.com/reliant-labs/forge/internal/codegen"
)

// scanRoots lists every `//forge:root` struct in internal/app, read by the
// same discovery the composition graph is built from. A discovery error (a
// malformed directive) lists nothing: `forge generate` reports it.
func scanRoots(projectDir string) []shape {
	roots, err := codegen.DeclaredRoots(projectDir)
	if err != nil {
		return nil //nolint:nilerr // partial output beats no output
	}
	out := make([]shape, 0, len(roots))
	for _, r := range roots {
		migrate := r.Migrate
		if migrate == "" {
			migrate = "default"
		}
		detail := fmt.Sprintf("type=%s command=%s migrate=%s fields=%d",
			r.TypeName, strings.Join(r.Command, ","), migrate, len(r.Fields))
		if r.Adopt {
			detail += " adopt"
		}
		if len(r.RootOnly) > 0 {
			names := make([]string, 0, len(r.RootOnly))
			for _, p := range r.RootOnly {
				names = append(names, p.Name)
			}
			detail += " root-only=" + strings.Join(names, ",")
		}
		out = append(out, shape{"root", r.Name, r.File, r.Line, detail})
	}
	return out
}

// scanWorkloads lists every named workload literal under deploy/kcl, as its
// source writes it — no env is rendered, so the listing stays a line scan.
func scanWorkloads(projectDir string) []shape {
	sites := codegen.DeclaredWorkloadSites(projectDir)
	out := make([]shape, 0, len(sites))
	for _, s := range sites {
		kind := s.Kind
		if kind == "" {
			kind = codegen.WorkloadKindService
		}
		detail := "kind=" + kind
		if s.Build != "" {
			detail += " build=" + s.Build
		}
		if s.Image != "" {
			detail += " image=" + s.Image
		}
		out = append(out, shape{"workload", s.Name, s.File, s.Line, detail})
	}
	return out
}

// writeShapesJSON prints the shapes --kind and --grep select as one JSON
// array (`[]` when none match), in the order the text listing uses.
func writeShapesJSON(w io.Writer, shapes []shape, want map[string]bool, pat *regexp.Regexp) error {
	matched := []shape{}
	for _, s := range shapes {
		if want != nil && !want[s.Kind] {
			continue
		}
		if pat != nil && !pat.MatchString(s.Name) && !pat.MatchString(s.Detail) {
			continue
		}
		matched = append(matched, s)
	}
	enc := json.NewEncoder(w)
	enc.SetIndent("", "  ")
	return enc.Encode(matched)
}
