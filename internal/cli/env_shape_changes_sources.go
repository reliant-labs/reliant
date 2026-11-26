package cli

// The Forge Changes an env's shape declares: the project root's changes.yaml,
// plus the changes.yaml of every frontend the env builds from a pinned
// source (docs/proposals/changes-from-sources.md).
//
// A frontend built from another repository (`source = forge.GitSource {...}`)
// evaluates changes that repository declares: its own `forge generate` wrote
// the accessors its code calls from the changes.yaml at its root. The release
// that ships it is cut here, so without this the env would never declare
// them, and the control plane refuses to launch a change the env's release
// does not declare.
//
//   - Which file: changes.yaml at the source REPOSITORY root (not `subdir`),
//     the file that repository's own accessors come from.
//   - Which commit: the one the release pins for that frontend
//     (bundleBuildInputs.Pins.Sources) when there is one, else what the
//     declared ref resolves to, through the same resolver, cache and local
//     overrides the build uses. A shape never declares a change from other
//     bytes than the ones it ships.
//   - Validation: each file on its own, its errors naming its origin
//     (repo@commit:changes.yaml:<line>), then the merged set: one definition
//     per key and at most release.MaxShapeChanges in all.
//   - One key, two declarations: identical ones (type, default, variants;
//     the description aside) are kept once. Different ones are the rule
//     changes-source-conflict, warn-first (lint.GradeRule): while it warns,
//     the first declaration ships (the root's, else the first source by
//     frontend name) and the conflict is printed; when it gates, the shape
//     is refused.
//   - Opt-out: `changes_from_source = False` on the frontend.
//
// A project with no frontend built from a source, or whose sources declare
// nothing, gets exactly the root's declaration, so its shape and shape digest
// are byte-identical to before. Nothing is resolved for it.

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/changesdecl"
	"github.com/reliant-labs/forge/internal/cli/lint"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/gitsource"
	"github.com/reliant-labs/forge/pkg/release"
)

// changeSourceResolver is what reading a source's changes.yaml needs of
// internal/gitsource: a pin resolved to a directory.
type changeSourceResolver interface {
	Resolve(ctx context.Context, src gitsource.Source) (gitsource.Resolution, error)
}

// newChangeSourceResolver builds the resolver the shape reads sources with:
// the build's (frontendSourceResolver: the machine cache and the project's
// .forge/source-overrides.yaml), logging to errOut so `forge env shape
// --json` keeps stdout to the document. A variable for the tests.
var newChangeSourceResolver = func(projectDir string, errOut io.Writer) (changeSourceResolver, error) {
	return gitsource.NewResolver(projectDir, gitsource.WithLogger(func(line string) { fmt.Fprintln(errOut, line) }))
}

// changeSourcePinsKey carries a bundle's source pins (frontend name → the
// source its release recorded) to the shape projection.
type changeSourcePinsKey struct{}

// withChangeSourcePins makes the shape read each sourced frontend's
// changes.yaml at the commit pins records for it. Empty pins change nothing.
func withChangeSourcePins(ctx context.Context, pins map[string]release.Source) context.Context {
	if len(pins) == 0 {
		return ctx
	}
	return context.WithValue(ctx, changeSourcePinsKey{}, pins)
}

func changeSourcePinsFrom(ctx context.Context) map[string]release.Source {
	pins, _ := ctx.Value(changeSourcePinsKey{}).(map[string]release.Source)
	return pins
}

// changesDeclaration is one changes.yaml and where it came from.
type changesDeclaration struct {
	// Origin is "changes.yaml" for the root, "<repo>@<commit>" (or the
	// ref, or "<repo> (local override)") for a source.
	Origin  string
	Changes map[string]release.ShapeChange
}

// envShapeChanges is the env's declared changes: the root's, then each
// sourced frontend's (sourcedChangeFrontends), merged by mergeShapeChanges,
// and the origin of each change a source declared (nil when none did). The
// origins are for `forge env shape` to print, never part of the shape.
func envShapeChanges(ctx context.Context, errOut io.Writer, projectDir string, entities *KCLEntities) (map[string]release.ShapeChange, map[string]string, error) {
	root, err := changesdecl.Load(projectDir)
	if err != nil {
		return nil, nil, err
	}
	var frontends []FrontendEntity
	if entities != nil {
		frontends = entities.Frontends
	}
	sourced := sourcedChangeFrontends(frontends)
	if len(sourced) == 0 {
		return root.Shape(), nil, nil
	}
	resolver, err := newChangeSourceResolver(projectDir, errOut)
	if err != nil {
		return nil, nil, err
	}
	decls := []changesDeclaration{{Origin: changesdecl.FileName, Changes: root.Shape()}}
	more, err := sourceChangesDeclarations(ctx, resolver, sourced, changeSourcePinsFrom(ctx))
	if err != nil {
		return nil, nil, err
	}
	decls = append(decls, more...)
	cfg, cerr := config.LoadProjectDir(projectDir)
	if cerr != nil {
		cfg = nil // no forge.yaml: the conflict rule gates (lint.GradeRule)
	}
	merged, origins, err := mergeShapeChanges(errOut, lint.GradeRule(cfg, lint.RuleChangesSourceConflict), decls)
	if err != nil {
		return nil, nil, err
	}
	for k, o := range origins {
		if o == changesdecl.FileName {
			delete(origins, k)
		}
	}
	if len(origins) == 0 {
		origins = nil
	}
	return merged, origins, nil
}

// sourcedChangeFrontends are the frontends whose source's changes the env
// carries: built from a pinned source, not opted out, by name.
func sourcedChangeFrontends(frontends []FrontendEntity) []FrontendEntity {
	var out []FrontendEntity
	for _, fe := range frontends {
		if fe.Source == nil || fe.Source.Repo == "" {
			continue
		}
		if fe.ChangesFromSource != nil && !*fe.ChangesFromSource {
			continue
		}
		out = append(out, fe)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

// sourceChangesDeclarations reads each frontend's source changes.yaml, once
// per distinct checkout. A source with no changes.yaml declares nothing.
func sourceChangesDeclarations(ctx context.Context, r changeSourceResolver, frontends []FrontendEntity, pins map[string]release.Source) ([]changesDeclaration, error) {
	var out []changesDeclaration
	seen := map[string]bool{}
	for _, fe := range frontends {
		dir, origin, err := resolveChangeSource(ctx, r, fe, pins[fe.Name])
		if err != nil {
			return nil, fmt.Errorf("frontend %q: read the Forge Changes its source declares: %w", fe.Name, err)
		}
		if seen[dir] {
			continue
		}
		seen[dir] = true
		data, err := os.ReadFile(filepath.Join(dir, changesdecl.FileName))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, fmt.Errorf("frontend %q: read %s:%s: %w", fe.Name, origin, changesdecl.FileName, err)
		}
		decl, err := changesdecl.Parse(origin+":"+changesdecl.FileName, data)
		if err != nil {
			return nil, fmt.Errorf("frontend %q: %w", fe.Name, err)
		}
		if shape := decl.Shape(); len(shape) > 0 {
			out = append(out, changesDeclaration{Origin: origin, Changes: shape})
		}
	}
	return out, nil
}

// resolveChangeSource is the repository root of fe's source at the commit
// its release pins (pin), else at its declared ref, and that checkout's
// origin label. Reject a mismatched repository before resolving anything:
// reading the declared source would not describe the frontend the bundle ships.
func resolveChangeSource(ctx context.Context, r changeSourceResolver, fe FrontendEntity, pin release.Source) (string, string, error) {
	if pin.Repo != "" && pin.Repo != fe.Source.Repo {
		return "", "", fmt.Errorf("the release pins repository %q but the frontend declares %q; align the frontend source and release pin", pin.Repo, fe.Source.Repo)
	}
	src := gitsource.Source{Repo: fe.Source.Repo, Ref: fe.Source.Ref}
	if pin.Commit != "" {
		src.Ref = pin.Commit
	}
	res, err := r.Resolve(ctx, src)
	if err != nil {
		return "", "", err
	}
	if res.Overridden {
		return res.Dir, fe.Source.Repo + " (local override)", nil
	}
	at := res.Commit
	if at == "" {
		at = src.Ref
	}
	return res.Dir, fe.Source.Repo + "@" + changeSourceAt(at), nil
}

// changeSourceAt shortens a full sha to 12 characters and leaves a ref alone.
func changeSourceAt(s string) string {
	if len(s) == 40 && strings.Trim(s, "0123456789abcdef") == "" {
		return s[:12]
	}
	return s
}

// changeConflict is one key two declarations define differently.
type changeConflict struct {
	Key, Kept, Other string
	Why              string
}

// mergeShapeChanges folds decls, in order, into one declaration, and names
// the origin of each kept change. See the file header for the rules; grade is
// the changes-source-conflict rule's.
func mergeShapeChanges(errOut io.Writer, grade lint.RuleGrade, decls []changesDeclaration) (map[string]release.ShapeChange, map[string]string, error) {
	merged := map[string]release.ShapeChange{}
	origin := map[string]string{}
	var conflicts []changeConflict
	for _, d := range decls {
		keys := make([]string, 0, len(d.Changes))
		for k := range d.Changes {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			c := d.Changes[k]
			have, ok := merged[k]
			if !ok {
				merged[k], origin[k] = c, d.Origin
				continue
			}
			if why := changeDifference(have, c); why != "" {
				conflicts = append(conflicts, changeConflict{Key: k, Kept: origin[k], Other: d.Origin, Why: why})
			}
		}
	}
	if len(conflicts) > 0 {
		lines := make([]string, 0, len(conflicts))
		for _, c := range conflicts {
			lines = append(lines, fmt.Sprintf("change %q is declared by %s and by %s, differently (%s)", c.Key, c.Kept, c.Other, c.Why))
		}
		fix := "rename the key in one of them, or make the two declarations equal"
		if grade.Gates {
			return nil, nil, fmt.Errorf("[%s] %d Forge Changes key(s) are declared twice:\n  %s\n  fix: %s",
				grade.Rule, len(conflicts), strings.Join(lines, "\n  "), fix)
		}
		for _, l := range lines {
			fmt.Fprintf(errOut, "warning: [%s] %s; the shape keeps the first\n", grade.Rule, l)
		}
		fmt.Fprintf(errOut, "warning: [%s] %s; fix: %s; gate now with %s\n", grade.Rule, grade.Note(), fix, grade.OptIn())
	}
	if len(merged) > release.MaxShapeChanges {
		counts := make([]string, 0, len(decls))
		for _, d := range decls {
			if len(d.Changes) > 0 {
				counts = append(counts, fmt.Sprintf("%s declares %d", d.Origin, len(d.Changes)))
			}
		}
		return nil, nil, fmt.Errorf("the env declares %d Forge Changes (at most %d): %s", len(merged), release.MaxShapeChanges, strings.Join(counts, ", "))
	}
	if len(merged) == 0 {
		return nil, nil, nil
	}
	return merged, origin, nil
}

// changeDifference says how b's definition differs from a's ("" when they
// are the same change). The description is prose, not the change.
func changeDifference(a, b release.ShapeChange) string {
	switch {
	case a.Type != b.Type:
		return fmt.Sprintf("type %s vs %s", a.Type, b.Type)
	case a.Default != b.Default:
		return fmt.Sprintf("default %s vs %s", a.Default, b.Default)
	case len(a.Variants) != len(b.Variants):
		return fmt.Sprintf("%d variants vs %d", len(a.Variants), len(b.Variants))
	}
	names := make([]string, 0, len(a.Variants))
	for n := range a.Variants {
		names = append(names, n)
	}
	sort.Strings(names)
	for _, n := range names {
		bv, ok := b.Variants[n]
		if !ok {
			return fmt.Sprintf("variant %s only in the first", n)
		}
		if !sameJSON(a.Variants[n], bv) {
			return fmt.Sprintf("variant %s is %s vs %s", n, a.Variants[n], bv)
		}
	}
	return ""
}

func sameJSON(a, b json.RawMessage) bool {
	var ca, cb bytes.Buffer
	if json.Compact(&ca, a) != nil || json.Compact(&cb, b) != nil {
		return bytes.Equal(a, b)
	}
	return bytes.Equal(ca.Bytes(), cb.Bytes())
}
