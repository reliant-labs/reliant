package obsdash

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/atomicfile"

	"github.com/reliant-labs/forge/internal/checksums"
)

const (
	// DashboardsDir is the project-relative directory mounted as HyperDX's
	// DASHBOARD_PROVISIONER_DIR.
	DashboardsDir = "deploy/observability/dashboards"
	// SpecDir holds the serialized Spec, the renderer-neutral model, as its
	// parts: ProjectSpecFile and one ProcessSpecFile per service (see
	// ProcessPart for why it is not one file). It sits beside the dashboards
	// directory, not in it, because the provisioner reads every *.json it
	// finds there. ReadSpec assembles it.
	SpecDir = "deploy/observability/spec"
	// ProjectSpecFile is the project's ProjectPart.
	ProjectSpecFile = SpecDir + "/project" + genSuffix

	// LegacySpecFile is the whole-project spec the parts replace. Write
	// retires it when pristine, as it does the whole-project
	// saved-searches_gen.ndjson, an unwritten *_gen.ndjson like any other.
	LegacySpecFile = "deploy/observability/dashboard-spec_gen.json"
)

// ProcessSpecFile is the project-relative file of service's ProcessPart.
func ProcessSpecFile(service string) string {
	return SpecDir + "/process-" + service + genSuffix
}

// SavedSearchesFile is the project-relative file holding service's HyperDX
// saved searches, one JSON object per line, named like the service's
// dashboard. It lives in the dashboards directory (the one directory compose
// mounts) under an extension the dashboard provisioner ignores; the compose
// entrypoint upserts every *_gen.ndjson there, because the file provisioner
// has no saved-search path. One file per service, for the reason in
// ProcessPart.
func SavedSearchesFile(project, service string) string {
	return DashboardsDir + "/" + project + "-process-" + service + ndjsonGenSuffix
}

// genSuffix marks the files forge owns and rewrites; ndjsonGenSuffix the
// saved-search ones.
const (
	genSuffix       = "_gen.json"
	ndjsonGenSuffix = "_gen.ndjson"
)

// hostedOnly reports whether d is a dashboard with nothing to draw in the local
// stack, and so no HyperDX file: Workloads reads what a node agent delivers,
// Jobs the Kubernetes Job events it delivers, and Releases reads the release ledger, and neither exists under
// `forge env up`. They are also constant text, independent of the project's
// services, so writing them would put library code in the project's tree
// behind a do-not-edit banner. They ship in the spec and in the release bundle.
func hostedOnly(d Dashboard) bool { return d.ID == WorkloadsID || d.ID == ReleasesID || d.ID == JobsID }

// OverridePath is the file that, when present, replaces the generated
// dashboard d: forge neither writes nor refreshes its _gen twin. The file is
// the project's, and forge never touches it.
func OverridePath(project string, d Dashboard) string {
	return DashboardsDir + "/" + project + "-" + d.ID + ".json"
}

// Write generates the project's dashboards under projectDir through the
// checksummed writer (a hand-edited generated file is left alone, exactly like
// every other Tier-1 output). It returns the project-relative paths written
// or kept.
func Write(projectDir string, in Input, cs *checksums.FileChecksums) ([]string, error) {
	project, parts := BuildParts(in)
	sp := Assemble(project, parts)

	files, err := RenderHyperDX(sp)
	if err != nil {
		return nil, err
	}
	byFile := map[string]Dashboard{}
	for _, d := range sp.Dashboards {
		byFile[DashboardFile(sp.Project, d)] = d
	}

	var out []string
	emit := func(rel string, content []byte) error {
		if _, err := checksums.WriteGeneratedFile(projectDir, rel, content, cs, true); err != nil {
			return fmt.Errorf("write %s: %w", rel, err)
		}
		out = append(out, rel)
		return nil
	}

	for _, f := range files {
		d := byFile[f.File]
		if hostedOnly(d) {
			continue
		}
		rel := DashboardsDir + "/" + f.File
		if d.ID == "overview" {
			owned := OverridePath(sp.Project, d)
			undeployed, err := undeployedOverview(in, f.File)
			if err != nil {
				return nil, err
			}
			if err := writeOwnedOverview(projectDir, owned, rel, f.Content, undeployed, cs); err != nil {
				return nil, err
			}
			out = append(out, owned)
			continue
		}
		if _, err := os.Stat(filepath.Join(projectDir, OverridePath(sp.Project, d))); err == nil {
			if err := retireGenerated(projectDir, rel, cs); err != nil {
				return nil, err
			}
			continue
		}
		if err := emit(rel, f.Content); err != nil {
			return nil, err
		}
	}

	if err := emitJSON(emit, ProjectSpecFile, project); err != nil {
		return nil, err
	}
	for _, part := range parts {
		if err := emitJSON(emit, ProcessSpecFile(part.Process.Name), part); err != nil {
			return nil, err
		}
		searches, err := RenderHyperDXSavedSearches(Spec{Project: sp.Project, SavedSearches: part.SavedSearches})
		if err != nil {
			return nil, err
		}
		var nd bytes.Buffer
		for _, s := range searches {
			b, err := json.Marshal(s)
			if err != nil {
				return nil, err
			}
			nd.Write(b)
			nd.WriteByte('\n')
		}
		if err := emit(SavedSearchesFile(sp.Project, part.Process.Name), nd.Bytes()); err != nil {
			return nil, err
		}
	}

	// Alert service inventory remains disjoint per RPC service, independently
	// of dashboard ownership. Adding an RPC must not conflict in project.json.
	for _, s := range AlertInputOf(in).Services {
		part := alertServicePart{Version: PartsVersion, Alert: s}
		if err := emitJSON(emit, alertServiceSpecFile(s.Name), part); err != nil {
			return nil, err
		}
	}
	alertDefaults, err := MarshalAlertDefaults(AlertInputOf(in))
	if err != nil {
		return nil, err
	}
	if err := emit(AlertDefaultsFile, alertDefaults); err != nil {
		return nil, err
	}

	// Outputs the project no longer has (a removed service, or the
	// whole-project aggregates the parts replaced) leave a pristine
	// generated file behind; drop exactly the ones forge wrote.
	if err := retireStale(projectDir, out, cs); err != nil {
		return nil, err
	}

	sort.Strings(out)
	return out, nil
}

// emitJSON writes v as indented JSON through emit.
func emitJSON(emit func(string, []byte) error, rel string, v any) error {
	b, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return emit(rel, append(b, '\n'))
}

// undeployedOverview is the overview's HyperDX file as forge renders it when it
// knows no deployed process: every identity predicate on the
// no-deployed-process sentinel. An owned overview still byte-equal to it is an
// untouched scaffold that froze that answer, not a user's dashboard.
func undeployedOverview(in Input, file string) ([]byte, error) {
	in.Deployed = Identities{}
	in.Processes = nil
	project, parts := BuildParts(in)
	files, err := RenderHyperDX(Assemble(project, parts))
	if err != nil {
		return nil, err
	}
	for _, f := range files {
		if f.File == file {
			return f.Content, nil
		}
	}
	return nil, nil
}

// writeOwnedOverview gives the local overview the same ownership as an
// explicit dashboard override. Edits to a legacy generated file are migrated
// verbatim before retiring its old name; an existing owned override wins.
//
// Two cases are NOT the user's edits and so are not preserved: a legacy file
// that is pristine forge output, and an owned file still byte-equal to the
// undeployed render (undeployed). Both froze the overview's identity scope as
// the no-deployed-process sentinel, typically because `forge project new`'s
// first generate ran before any environment's workloads rendered; once owned,
// no later generate refreshed it, and every panel of the local overview drew
// nothing forever. They take the current render instead.
func writeOwnedOverview(root, owned, legacy string, content, undeployed []byte, cs *checksums.FileChecksums) error {
	old, err := os.ReadFile(filepath.Join(root, legacy))
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		return err
	}
	if err != nil {
		old = nil
	}
	pristine := old != nil && cs != nil && cs.Unstampable[legacy] == checksums.BodyHash(old)
	if old != nil && !pristine {
		content = old
	}
	written, err := checksums.WriteScaffoldIfMissing(root, owned, content)
	if err != nil {
		return err
	}
	if !written && undeployed != nil && !bytes.Equal(content, undeployed) {
		onDisk, err := os.ReadFile(filepath.Join(root, owned))
		if err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		if err == nil && bytes.Equal(onDisk, undeployed) {
			checksums.RecordPreWrite(root, owned)
			if err := atomicfile.WriteFile(filepath.Join(root, owned), content, 0o644); err != nil {
				return err
			}
			checksums.MarkWrittenThisRun(owned)
		}
	}
	// A deliberately deleted scaffold must not resurrect or erase its legacy
	// source. Only retire after the owned destination is present.
	if _, err := os.Stat(filepath.Join(root, owned)); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return nil
		}
		return err
	}
	if old != nil {
		// The pre-emitter guard may already have quarantined this legacy
		// path. Record the explicit one-way ownership transfer so final
		// adjudication does not demand a render for a now user-owned file.
		if cs != nil {
			if err := cs.DisownPaths(root, []string{legacy}, "constant overview migrated to owned scaffold"); err != nil {
				return err
			}
		}
		destination, err := os.ReadFile(filepath.Join(root, owned))
		if err != nil {
			return err
		}
		if !bytes.Equal(destination, old) && !pristine {
			checksums.RecordPreservedOwnershipTransfer(root, legacy, owned, old)
			return nil // Two independent edits: preserve both, never discard one.
		}
		checksums.RecordPreservedOwnershipTransfer(root, legacy, owned, old)
		if err := checksums.RemoveJournaled(filepath.Join(root, legacy)); err != nil {
			return err
		}
		if cs != nil {
			delete(cs.Unstampable, legacy)
		}
	}
	return nil
}

// retireGenerated removes rel when it is a pristine forge render.
func retireGenerated(projectDir, rel string, cs *checksums.FileChecksums) error {
	abs := filepath.Join(projectDir, rel)
	onDisk, err := os.ReadFile(abs)
	if err != nil {
		return nil
	}
	if cs == nil || cs.Unstampable[rel] != checksums.BodyHash(onDisk) {
		return nil
	}
	if err := checksums.RemoveJournaled(abs); err != nil {
		return err
	}
	delete(cs.Unstampable, rel)
	return nil
}

// retireStale removes every pristine generated file under the dashboards
// directory and SpecDir that this run did not write (written), plus the
// legacy whole-project spec.
func retireStale(projectDir string, written []string, cs *checksums.FileChecksums) error {
	want := map[string]bool{}
	for _, rel := range written {
		want[rel] = true
	}
	for _, dir := range []string{DashboardsDir, SpecDir} {
		entries, err := os.ReadDir(filepath.Join(projectDir, filepath.FromSlash(dir)))
		if err != nil {
			continue
		}
		for _, e := range entries {
			rel := dir + "/" + e.Name()
			if e.IsDir() || want[rel] || !(strings.HasSuffix(rel, genSuffix) || strings.HasSuffix(rel, ndjsonGenSuffix)) {
				continue
			}
			if err := retireGenerated(projectDir, rel, cs); err != nil {
				return err
			}
		}
	}
	return retireGenerated(projectDir, LegacySpecFile, cs)
}

// ReadFragments reads reusable generation input without loading old process
// parts. This is the only disk input selected-env assembly consumes.
func ReadFragments(projectDir string) (Input, bool, error) {
	raw, err := os.ReadFile(filepath.Join(projectDir, ProjectSpecFile))
	if errors.Is(err, os.ErrNotExist) {
		return Input{}, false, nil
	}
	if err != nil {
		return Input{}, false, err
	}
	var p ProjectPart
	if err := decodePart(ProjectSpecFile, raw, &p, &p.Version); err != nil {
		return Input{}, false, err
	}
	if p.Input == nil {
		return Input{}, false, fmt.Errorf("%s has no generation fragments: run forge generate", ProjectSpecFile)
	}
	f := p.Input
	return Input{Project: p.Project, Frontends: f.Frontends, FrontendServices: f.FrontendServices, HasDatabase: f.HasDatabase, Funnels: f.Funnels}, true, nil
}

// ReadSpec assembles the Spec forge generate last wrote under projectDir.
// ok is false when there is none (a project that has not generated
// dashboards). A part of another version is an error that names the fix.
func ReadSpec(projectDir string) (sp Spec, ok bool, err error) {
	project, parts, ok, err := readParts(projectDir)
	if err != nil || !ok {
		return Spec{}, ok, err
	}
	return Assemble(project, parts), true, nil
}

// readParts reads ProjectSpecFile and every ProcessSpecFile.
func readParts(projectDir string) (ProjectPart, []ProcessPart, bool, error) {
	var project ProjectPart
	raw, err := os.ReadFile(filepath.Join(projectDir, filepath.FromSlash(ProjectSpecFile)))
	if errors.Is(err, os.ErrNotExist) {
		return ProjectPart{}, nil, false, nil
	}
	if err != nil {
		return ProjectPart{}, nil, false, err
	}
	if err := decodePart(ProjectSpecFile, raw, &project, &project.Version); err != nil {
		return ProjectPart{}, nil, false, err
	}
	matches, err := filepath.Glob(filepath.Join(projectDir, filepath.FromSlash(SpecDir), "process-*"+genSuffix))
	if err != nil {
		return ProjectPart{}, nil, false, err
	}
	sort.Strings(matches)
	parts := make([]ProcessPart, 0, len(matches))
	for _, m := range matches {
		rel := SpecDir + "/" + filepath.Base(m)
		raw, err := os.ReadFile(m)
		if err != nil {
			return ProjectPart{}, nil, false, err
		}
		var part ProcessPart
		if err := decodePart(rel, raw, &part, &part.Version); err != nil {
			return ProjectPart{}, nil, false, err
		}
		parts = append(parts, part)
	}
	return project, parts, true, nil
}

// decodePart unmarshals one part file into v and checks the version it read
// into version.
func decodePart(rel string, raw []byte, v any, version *string) error {
	if err := json.Unmarshal(raw, v); err != nil {
		return fmt.Errorf("%s: %w", rel, err)
	}
	if *version != PartsVersion {
		return fmt.Errorf("%s: version %q, this forge reads %q: run `forge generate`", rel, *version, PartsVersion)
	}
	return nil
}
