package cigate

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/goexec"
)

// Go-closure gating: an extra check's `paths_go_closure` names Go packages
// (`./tools/docgen/...`) whose build the check depends on — a generator, a
// docs tool. On a branch in affected mode the check runs when a changed file
// is in those packages' build closure (`go list -deps`: every module package
// they compile, with its Go and embedded files), besides any `paths` glob it
// matches. A generator's closure is often most of the module, which no
// hand-kept glob list tracks.

// ClosureLister runs `go list -deps -json` over patterns in dir.
type ClosureLister func(ctx context.Context, dir string, env []string, patterns []string) (io.Reader, error)

// ResolveGoClosures records, for each check declaring GoClosure, the changed
// files in its closure (a.ClosureHits). It does nothing in full mode (no
// changed files). A closure go list cannot compute is recorded in
// a.ClosureErrs, and GateByPaths then runs the check: unknown reach is never
// N/A. A nil list runs the real go list.
func ResolveGoClosures(ctx context.Context, root string, env []string, checks []Check, a Affected, list ClosureLister) Affected {
	if len(a.Changed) == 0 {
		return a
	}
	if list == nil {
		list = runClosureList
	}
	absRoot, err := filepath.Abs(root)
	if err != nil {
		absRoot = root
	}
	realRoot := absRoot
	if r, rerr := filepath.EvalSymlinks(absRoot); rerr == nil {
		realRoot = r
	}
	for _, c := range checks {
		if len(c.GoClosure) == 0 || c.NotApplicable != "" || c.Deferred != "" {
			continue
		}
		if a.ClosureHits == nil {
			a.ClosureHits, a.ClosureErrs = map[string][]string{}, map[string]string{}
		}
		r, err := list(ctx, root, env, c.GoClosure)
		if err != nil {
			a.ClosureErrs[c.Name] = "go list failed: " + firstLine(err.Error())
			continue
		}
		files, dirs, err := readClosure(r, absRoot, realRoot)
		if err != nil {
			a.ClosureErrs[c.Name] = "cannot read go list output: " + err.Error()
			continue
		}
		var hits []string
		for _, f := range a.Changed {
			rel := filepath.ToSlash(f)
			// go.mod and go.sum decide every package's build; a .go file in
			// a closure package's directory (added, deleted, build-tagged
			// out) may change it too.
			if files[rel] || rel == "go.mod" || rel == "go.sum" ||
				strings.HasSuffix(rel, ".go") && !strings.HasSuffix(rel, "_test.go") && dirs[filepath.ToSlash(filepath.Dir(rel))] {
				hits = append(hits, f)
			}
		}
		a.ClosureHits[c.Name] = hits
	}
	return a
}

// readClosure reads `go list -deps -json` into the project-relative files
// (Go and embedded) and package directories of the module's own packages.
func readClosure(r io.Reader, absRoot, realRoot string) (files, dirs map[string]bool, err error) {
	files, dirs = map[string]bool{}, map[string]bool{}
	dec := json.NewDecoder(r)
	for {
		var p struct {
			Dir        string   `json:"Dir"`
			Standard   bool     `json:"Standard"`
			GoFiles    []string `json:"GoFiles"`
			CgoFiles   []string `json:"CgoFiles"`
			EmbedFiles []string `json:"EmbedFiles"`
		}
		if err := dec.Decode(&p); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return nil, nil, err
		}
		if p.Standard || p.Dir == "" {
			continue
		}
		dir := filepath.Clean(p.Dir)
		base := ""
		switch {
		case within(absRoot, dir):
			base = absRoot
		case within(realRoot, dir):
			base = realRoot
		default:
			continue // the module cache, a sibling checkout: not this diff
		}
		rel, rerr := filepath.Rel(base, dir)
		if rerr != nil {
			continue
		}
		rel = filepath.ToSlash(rel)
		dirs[rel] = true
		for _, group := range [][]string{p.GoFiles, p.CgoFiles, p.EmbedFiles} {
			for _, f := range group {
				files[filepath.ToSlash(filepath.Join(rel, f))] = true
			}
		}
	}
	return files, dirs, nil
}

func runClosureList(ctx context.Context, dir string, env []string, patterns []string) (io.Reader, error) {
	args := append([]string{"list", "-e", "-deps", "-json=Dir,Standard,GoFiles,CgoFiles,EmbedFiles", "--"}, patterns...)
	cmd := exec.CommandContext(ctx, "go", args...)
	cmd.Dir = dir
	if env == nil {
		env = goexec.Env()
	}
	cmd.Env = env
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("%w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return bytes.NewReader(out), nil
}
