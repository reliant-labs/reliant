package vulnpatch

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/vulnscan"
)

// ErrRefused is Apply's answer for a plan with a refusal.
var ErrRefused = errors.New("refused")

// Apply runs every planned bump with the ecosystem's own tool, one manifest
// at a time: `go get` then `go mod tidy` in a Go module; an override in
// package.json then an install, and an update of the direct dependencies,
// beside a lockfile. A failed command fails that manifest's bumps (with the
// tool's output) and the next manifest still runs: one broken module must
// not hide what the others needed.
func (p *Plan) Apply(ctx context.Context, run vulnscan.Runner) error {
	if len(p.Refusals) > 0 {
		return fmt.Errorf("%w: %s", ErrRefused, strings.Join(p.Refusals, "; "))
	}
	if p.applied {
		return errors.New("plan already applied")
	}
	p.applied = true
	if run == nil {
		run = vulnscan.Exec
	}
	var order []string
	groups := map[string][]*plannedBump{}
	for _, b := range p.bumps {
		if _, ok := groups[b.manifest]; !ok {
			order = append(order, b.manifest)
		}
		groups[b.manifest] = append(groups[b.manifest], b)
	}
	for _, m := range order {
		bs := groups[m]
		cmds, edits, err := applyManifest(ctx, run, bs)
		for _, b := range bs {
			b.done = &Bump{Ecosystem: b.eco, Manifest: b.manifest, Package: b.pkg, From: b.from, To: b.to, Direct: b.direct, Commands: cmds, Edits: edits}
			if err != nil {
				b.done.Error = err.Error()
				for _, r := range b.results {
					p.failed[r] = true
				}
			}
		}
	}
	for i := range p.Results {
		r := &p.Results[i]
		if r.Status != StatusBumped {
			continue
		}
		if p.failed[i] {
			r.Status = StatusFailed
			r.Reason = "the bump failed: " + p.bumpErrors(i)
		}
	}
	return nil
}

func (p *Plan) bumpErrors(result int) string {
	var errs []string
	for _, b := range p.bumps {
		if b.done == nil || b.done.Error == "" {
			continue
		}
		for _, r := range b.results {
			if r == result {
				errs = appendUnique(errs, b.manifest+": "+firstLine(b.done.Error))
			}
		}
	}
	return strings.Join(errs, "; ")
}

// applyManifest bumps every package of one manifest; bs share it.
func applyManifest(ctx context.Context, run vulnscan.Runner, bs []*plannedBump) (cmds [][]string, edits []string, err error) {
	first := bs[0]
	exec := func(name string, args ...string) error {
		argv := append([]string{name}, args...)
		cmds = append(cmds, argv)
		var out bytes.Buffer
		err := run(ctx, vulnscan.Command{Path: name, Args: args, Dir: first.dir, Stdout: &out, Stderr: &out})
		if err != nil {
			return fmt.Errorf("%s: %v\n%s", strings.Join(argv, " "), err, Tail(out.String(), 2000))
		}
		return nil
	}
	if first.eco == EcosystemGo {
		args := []string{"get"}
		for _, b := range bs {
			args = append(args, b.pkg+"@"+b.to)
		}
		if err := exec("go", args...); err != nil {
			return cmds, nil, err
		}
		return cmds, nil, exec("go", "mod", "tidy")
	}

	var direct []string
	overrides := map[string]string{}
	for _, b := range bs {
		if b.direct {
			direct = append(direct, b.pkg+"@"+b.to)
		} else {
			overrides[b.pkg] = b.to
		}
	}
	if len(overrides) > 0 {
		path := overridePath[first.eco]
		pkgJSON := filepath.Join(first.dir, "package.json")
		if err := setPackageJSON(pkgJSON, path, overrides); err != nil {
			return cmds, edits, fmt.Errorf("set %s in package.json: %w", strings.Join(path, "."), err)
		}
		for _, k := range sortedKeys(overrides) {
			edits = append(edits, fmt.Sprintf("package.json %s[%q] = %q", strings.Join(path, "."), k, overrides[k]))
		}
		if err := exec(string(first.eco), "install"); err != nil {
			return cmds, edits, err
		}
	}
	if len(direct) > 0 {
		var verb []string
		switch {
		case first.eco == EcosystemNPM:
			verb = []string{"install"}
		case first.eco == EcosystemPNPM:
			verb = []string{"update"}
		case first.berry:
			verb = []string{"up"}
		default:
			verb = []string{"upgrade"} // yarn 1
		}
		if err := exec(string(first.eco), append(verb, direct...)...); err != nil {
			return cmds, edits, err
		}
	}
	return cmds, edits, nil
}

// Bumps are the bumps applied, in plan order; before Apply, the planned ones
// with no commands.
func (p *Plan) Bumps() []Bump {
	out := []Bump{}
	for _, b := range p.bumps {
		if b.done != nil {
			out = append(out, *b.done)
			continue
		}
		out = append(out, Bump{Ecosystem: b.eco, Manifest: b.manifest, Package: b.pkg, From: b.from, To: b.to, Direct: b.direct, Commands: [][]string{}})
	}
	return out
}

// Tail is the last ~n bytes of s, from a line boundary.
func Tail(s string, n int) string {
	s = strings.TrimRight(s, "\n")
	if len(s) <= n {
		return s
	}
	s = s[len(s)-n:]
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[i+1:]
	}
	return "…\n" + s
}

func firstLine(s string) string {
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	return s
}
