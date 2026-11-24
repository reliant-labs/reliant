package cli

// A root module that consumes a producer module living in its own tree — a
// generated SDK (reliant's sdk/go) — at a PUBLISHED version, not through a
// replace (forge#2236).
//
// An additive proto change regenerates the producer, and the root's own
// generated code starts using what the producer now declares; but the root
// still compiles against the producer it pins, so the final validate build
// failed until a separate producer PR landed and the pin moved. The code was
// valid all along. A failure like that is a pending pin bump, and generate
// says so instead of failing — only when it is proven: the producer compiles
// on its own, and the root compiles against the producer as generated
// (a temporary -modfile replacing each pin with its directory). Anything
// else is the failure it always was.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/mod/modfile"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// pinnedProducer is a module in the project's tree that the root module
// requires at a version, with no replace pointing at the tree.
type pinnedProducer struct {
	Path, Dir, Version string
}

// pinnedProducers reads the root go.mod for them: a requirement on a module
// under the root's own path whose directory in the tree declares that module.
func pinnedProducers(projectDir string) []pinnedProducer {
	raw, err := os.ReadFile(filepath.Join(projectDir, "go.mod"))
	if err != nil {
		return nil
	}
	mf, err := modfile.ParseLax("go.mod", raw, nil)
	if err != nil || mf.Module == nil {
		return nil
	}
	root := mf.Module.Mod.Path
	replaced := map[string]bool{}
	for _, r := range mf.Replace {
		replaced[r.Old.Path] = true
	}
	var out []pinnedProducer
	for _, r := range mf.Require {
		rel, ok := strings.CutPrefix(r.Mod.Path, root+"/")
		if !ok || replaced[r.Mod.Path] {
			continue
		}
		dir := filepath.Join(projectDir, filepath.FromSlash(rel))
		sub, err := os.ReadFile(filepath.Join(dir, "go.mod"))
		if err != nil || modfile.ModulePath(sub) != r.Mod.Path {
			continue
		}
		out = append(out, pinnedProducer{Path: r.Mod.Path, Dir: dir, Version: r.Mod.Version})
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Path < out[j].Path })
	return out
}

// producerPinLag is the pending-pin-bump report for a root that failed to
// compile, and the root's compile against its producers as generated; ""
// when the failure is not (provably) a pin lag.
func producerPinLag(projectDir string) (string, compileReport) {
	producers := pinnedProducers(projectDir)
	if len(producers) == 0 {
		return "", compileReport{}
	}
	// 1. Each producer compiles on its own: its generated code is valid.
	for _, p := range producers {
		rep, err := compileTreeWith(p.Dir, false, nil, []string{"GOWORK=off"})
		if err != nil || rep.Build != "" {
			return "", compileReport{}
		}
	}
	// 2. The root compiles against them as generated.
	tmp, err := os.MkdirTemp("", "forge-producer-pin-*")
	if err != nil {
		return "", compileReport{}
	}
	defer func() { _ = os.RemoveAll(tmp) }()
	raw, err := os.ReadFile(filepath.Join(projectDir, "go.mod"))
	if err != nil {
		return "", compileReport{}
	}
	mf, err := modfile.Parse("go.mod", raw, nil)
	if err != nil {
		return "", compileReport{}
	}
	for _, p := range producers {
		dir, err := filepath.Abs(p.Dir) // the temporary go.mod is elsewhere
		if err != nil {
			return "", compileReport{}
		}
		if err := mf.AddReplace(p.Path, "", dir, ""); err != nil {
			return "", compileReport{}
		}
	}
	out, err := mf.Format()
	if err != nil {
		return "", compileReport{}
	}
	modPath := filepath.Join(tmp, "go.mod")
	if err := atomicfile.WriteFile(modPath, out, 0o644); err != nil {
		return "", compileReport{}
	}
	if sum, err := os.ReadFile(filepath.Join(projectDir, "go.sum")); err == nil {
		if err := atomicfile.WriteFile(filepath.Join(tmp, "go.sum"), sum, 0o644); err != nil {
			return "", compileReport{}
		}
	}
	// -mod=mod writes only the temporary go.mod/go.sum: a producer's own
	// requirements may need sums the root's go.sum does not carry yet.
	local, err := compileTreeWith(projectDir, true, []string{"-modfile=" + modPath, "-mod=mod"}, []string{"GOWORK=off"})
	if err != nil || local.Build != "" {
		return "", compileReport{}
	}
	return pinLagMessage(producers), local
}

func pinLagMessage(producers []pinnedProducer) string {
	var b strings.Builder
	b.WriteString("pending pin bump, not a generate failure: the root module compiles only against what generate just wrote into ")
	for i, p := range producers {
		if i > 0 {
			b.WriteString(", ")
		}
		fmt.Fprintf(&b, "%s (it pins %s %s)", filepath.Base(p.Dir), p.Path, p.Version)
	}
	b.WriteString(". Each producer compiles on its own, and the root compiles against it as generated, so the generated code is valid; " +
		"it waits only for the producer to be published and the pin moved (`go get <module>@<new version>`), which is what makes the root build as committed. " +
		"Until then a plain `go build ./...` of the root fails the same way.")
	return b.String()
}
