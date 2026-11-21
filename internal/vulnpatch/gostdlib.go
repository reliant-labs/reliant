package vulnpatch

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/mod/modfile"
	"golang.org/x/mod/semver"
)

// The Go standard library comes from the toolchain that built the binary,
// not from any module, so its patch is a newer toolchain in two places:
//
//   - the `toolchain` directive of every go.mod (and the root go.work) of the
//     tree that asks for less, with `go mod edit -toolchain=` (`go work
//     edit`): what a developer's and CI's GOTOOLCHAIN=auto build selects.
//     The `go` line is the module's language version and minimum, not the
//     toolchain: a toolchain at or above it is valid whatever it says, so it
//     is never raised.
//   - the golang builder image of every Dockerfile stage that builds on one:
//     the official images set GOTOOLCHAIN=local, so the image's Go is the
//     toolchain an image build uses whatever go.mod asks. golang:1.22.3-alpine
//     moves to golang:1.22.5-alpine (same shape and suffix, when the registry
//     has it); a two-part tag (1.22-alpine) already floats over patches, and
//     is moved only to another Go release, or re-pinned when it pins a
//     digest.
//
// A new Go release (1.22 -> 1.23) is a new minor that changes the language
// and the standard library, so, as for a base image, it counts as a major:
// only --allow-major crosses it.

// chooseGoTarget is ChooseTarget for a Go release: the lowest fixed release
// at or above installed, in installed's Go release (1.22.x) unless
// allowMajor. Versions are read with or without "go" (go1.22.3, 1.22.3).
func chooseGoTarget(installed string, fixed []string, allowMajor bool) (string, error) {
	cur := canonical(goBare(installed))
	if cur == "" {
		return "", &TargetError{Reason: fmt.Sprintf("installed Go version %q is not a release", installed)}
	}
	type cand struct{ raw, v string }
	var cands []cand
	for _, f := range fixed {
		v := canonical(goBare(f))
		if v == "" || semver.Prerelease(v) != "" || semver.Compare(v, cur) < 0 {
			continue
		}
		cands = append(cands, cand{raw: strings.TrimSpace(f), v: v})
	}
	if len(cands) == 0 {
		return "", &TargetError{Reason: fmt.Sprintf("no fixed Go release at or above %s (fixed in: %s)", installed, orNone(fixed))}
	}
	sort.Slice(cands, func(i, j int) bool { return semver.Compare(cands[i].v, cands[j].v) < 0 })
	for _, c := range cands {
		if semver.MajorMinor(c.v) == semver.MajorMinor(cur) {
			return c.raw, nil
		}
	}
	if allowMajor {
		return cands[0].raw, nil
	}
	return "", &TargetError{
		Reason: fmt.Sprintf("needs a new Go release: the lowest fix is %s, past %s's release (--allow-major takes it)", cands[0].raw, installed),
		Major:  cands[0].raw,
		err:    ErrNeedsMajor,
	}
}

// goBare is a Go version without its "go" prefix: 1.22.3.
func goBare(v string) string { return strings.TrimPrefix(strings.TrimSpace(v), "go") }

// compareGo orders two Go versions spelled with or without "go".
func compareGo(a, b string) int { return compareVersions(goBare(a), goBare(b)) }

// resolveGoStdlib plans the toolchain directives and the builder images a
// Go standard library fix needs. As for every finding, the fix is planned
// everywhere it is needed or nowhere: a go.mod forge cannot read, or a
// builder image it cannot move, skips the whole finding with the reason,
// since raising the rest would leave a tree that still builds with the
// vulnerable Go and a result that claims otherwise.
func (p *Plan) resolveGoStdlib(ctx context.Context, r *Result, idx *index, at int) {
	target := goBare(r.Target)
	tv := canonical(target)
	toolchain := "go" + strings.TrimPrefix(tv, "v")
	already := 0
	var unresolved []string
	var toolchains, images []*plannedBump

	// The toolchain directives.
	type goFile struct{ rel, dir, abs string }
	var files []goFile
	for _, m := range idx.goMods {
		files = append(files, goFile{rel: m.rel, dir: m.dir, abs: filepath.Join(m.dir, "go.mod")})
	}
	if idx.goWork {
		files = append(files, goFile{rel: "go.work", dir: p.Dir, abs: filepath.Join(p.Dir, "go.work")})
	}
	for _, gf := range files {
		goLine, tc, err := readGoDirectives(gf.abs)
		if err != nil {
			unresolved = append(unresolved, fmt.Sprintf("%s: unreadable: %v", gf.rel, err))
			continue
		}
		r.Manifests = append(r.Manifests, gf.rel)
		effective := goLine
		if tc != "" && compareGo(tc, effective) > 0 {
			effective = tc
		}
		if effective != "" && compareGo(effective, target) >= 0 {
			already++
			continue
		}
		from := tc
		switch {
		case from == "" && goLine != "":
			from = "go" + goLine
		case from == "":
			from = "none"
		}
		toolchains = append(toolchains, &plannedBump{eco: EcosystemGo, manifest: gf.rel, dir: gf.dir, pkg: "toolchain", from: from, to: toolchain, direct: true, toolchain: true})
	}

	// The golang builder images.
	for _, df := range idx.dockerfiles {
		for _, st := range df.stages {
			// A variable --platform (FROM --platform=$BUILDPLATFORM
			// golang:1.22.3) picks an architecture of the same tag, and
			// every one carries that tag's Go release: the literal tag is
			// still the one to move. A variable image names no release.
			if st.imageVariable() {
				if strings.Contains(strings.ToLower(st.image), "golang") {
					unresolved = append(unresolved, fmt.Sprintf("%s:%d builds FROM %s: a Go version named by a build argument is left to the agent", df.rel, st.line, st.image))
				}
				continue
			}
			ref, err := parseImageRef(st.image)
			if err != nil || !isGolangImage(ref) {
				continue
			}
			to, reason, err := p.golangBuilder(ctx, ref, tv)
			switch {
			case err != nil:
				unresolved = append(unresolved, fmt.Sprintf("%s:%d: could not read %s's registry: %v", df.rel, st.line, ref, err))
				continue
			case reason != "":
				unresolved = append(unresolved, fmt.Sprintf("%s:%d: %s", df.rel, st.line, reason))
				continue
			}
			r.Manifests = appendUnique(r.Manifests, df.rel)
			if to == nil {
				already++
				continue
			}
			images = append(images, &plannedBump{eco: EcosystemDocker, manifest: df.rel, dir: filepath.Dir(df.abs), pkg: ref.short(), from: ref.String(), to: to.String(), direct: true, line: st.line})
		}
	}

	switch {
	case len(unresolved) > 0:
		r.Manifests = nil
		r.Reason = strings.Join(unresolved, "; ")
		if n := len(toolchains) + len(images); n > 0 {
			r.Reason += fmt.Sprintf("; the %d other place(s) below Go %s are left too: the fix is made everywhere or nowhere", n, target)
		}
	case len(toolchains)+len(images) > 0:
		for _, b := range toolchains {
			p.planToolchain(b, at)
		}
		for _, b := range images {
			p.planImage(b, at)
		}
		r.Status, r.Reason = StatusBumped, ""
	case already > 0:
		r.Status, r.Reason = StatusFixed, fmt.Sprintf("the tree already builds with Go %s or later", target)
	default:
		r.Reason = "the Go standard library, and the tree has no go.mod, go.work or golang builder image to raise"
	}
}

// golangBuilder is the golang image a Dockerfile stage should build with to
// get Go tv (canonical, v1.22.5): nil when ref already does, and the reason
// when it cannot say.
func (p *Plan) golangBuilder(ctx context.Context, ref imageRef, tv string) (*imageRef, string, error) {
	tag := ref.tag
	if tag == "" {
		tag = "latest"
	}
	t, ok := parseImageTag(tag)
	if !ok || t.prefix != "" || t.parts < 2 {
		// golang, golang:alpine, golang:1: the tag floats to the newest
		// release, which a rebuild pulls; a digest pins it where it is.
		if ref.digest == "" {
			return nil, "", nil
		}
		return nil, fmt.Sprintf("%s names no Go release in its tag, and its digest pins one: left as is", ref), nil
	}
	want, _ := parseImageTag(strings.TrimPrefix(tv, "v"))
	out := ref
	switch {
	case t.parts == 3 && compareTags(t, want) >= 0,
		t.parts == 2 && (t.nums[0] > want.nums[0] || t.nums[0] == want.nums[0] && t.nums[1] > want.nums[1]):
		return nil, "", nil
	case t.parts == 3:
		out.tag = fmt.Sprintf("%d.%d.%d%s", want.nums[0], want.nums[1], want.nums[2], t.suffix)
	case t.nums[1] == want.nums[1]:
		// 1.22-alpine floats to the newest 1.22 already; a pinned digest
		// moves to the tag's current one.
		if ref.digest == "" {
			return nil, "", nil
		}
	default:
		out.tag = fmt.Sprintf("%d.%d%s", want.nums[0], want.nums[1], t.suffix)
	}
	d, err := p.digest(ctx, ref.repository(), out.tag)
	if errors.Is(err, ErrImageNotFound) && t.parts == 3 {
		// No image of the fixed release in this shape: a later patch of
		// the same Go release fixes it too.
		var later string
		if later, err = p.laterGolangTag(ctx, ref, t, want); err == nil && later != "" {
			out.tag = later
			d, err = p.digest(ctx, ref.repository(), out.tag)
		} else if err == nil {
			err = ErrImageNotFound
		}
	}
	if errors.Is(err, ErrImageNotFound) {
		return nil, fmt.Sprintf("%s:%s is not published: %s left as is", ref.name, out.tag, ref), nil
	}
	if err != nil {
		return nil, "", err
	}
	if ref.digest == "" {
		if out.tag == ref.tag {
			return nil, "", nil
		}
		return &out, "", nil
	}
	if d == ref.digest {
		return nil, "", nil
	}
	out.digest = d
	return &out, "", nil
}

// laterGolangTag is the lowest tag of ref's repository in t's Go release,
// shape and suffix at or above want, or "" when there is none.
func (p *Plan) laterGolangTag(ctx context.Context, ref imageRef, t, want imageTag) (string, error) {
	tags, err := p.tags(ctx, ref.repository())
	if err != nil {
		return "", err
	}
	var best *imageTag
	for _, raw := range tags {
		c, ok := parseImageTag(raw)
		if !ok || c.parts != 3 || c.prefix != t.prefix || c.suffix != t.suffix || c.nums[0] != want.nums[0] || c.nums[1] != want.nums[1] || compareTags(c, want) < 0 {
			continue
		}
		if best == nil || compareTags(c, *best) < 0 {
			best = &c
		}
	}
	if best == nil {
		return "", nil
	}
	return best.raw, nil
}

// planToolchain adds a toolchain bump, merging it into the manifest's
// other one: the newer Go wins.
func (p *Plan) planToolchain(b *plannedBump, result int) {
	for _, have := range p.bumps {
		if have.toolchain && have.manifest == b.manifest {
			if compareGo(b.to, have.to) > 0 {
				have.to = b.to
			}
			have.results = appendInt(have.results, result)
			return
		}
	}
	b.results = []int{result}
	p.bumps = append(p.bumps, b)
}

// isGolangImage reports the official golang image, on Docker Hub or a
// mirror of its library namespace.
func isGolangImage(ref imageRef) bool {
	return ref.path == "library/golang" || strings.HasSuffix(ref.path, "/library/golang")
}

// readGoDirectives is the go and toolchain lines of a go.mod or go.work.
func readGoDirectives(path string) (goLine, toolchain string, err error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return "", "", err
	}
	if filepath.Base(path) == "go.work" {
		f, err := modfile.ParseWork(path, data, nil)
		if err != nil {
			return "", "", err
		}
		if f.Go != nil {
			goLine = f.Go.Version
		}
		if f.Toolchain != nil {
			toolchain = f.Toolchain.Name
		}
		return goLine, toolchain, nil
	}
	// Parse, not ParseLax: the lax parser skips the toolchain directive.
	f, err := modfile.Parse(path, data, nil)
	if err != nil {
		return "", "", err
	}
	if f.Go != nil {
		goLine = f.Go.Version
	}
	if f.Toolchain != nil {
		toolchain = f.Toolchain.Name
	}
	return goLine, toolchain, nil
}

// toolchainCommand is the edit that sets a manifest's toolchain directive.
func toolchainCommand(manifest, toolchain string) []string {
	verb := "mod"
	if filepath.Base(manifest) == "go.work" {
		verb = "work"
	}
	return []string{verb, "edit", "-toolchain=" + toolchain}
}
