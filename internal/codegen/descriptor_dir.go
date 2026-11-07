package codegen

// The forge descriptor on disk: one file per .proto, under gen/forge_descriptor/,
// derived from proto/ ON DEMAND and never committed.
//
// WHY IT IS NOT COMMITTED. Only files a compiler needs to build the module
// are committed (the .pb.go and connect stubs, compiled *_gen.go); a file
// only forge or tooling reads is derived when it is needed
// (commitpolicy.IsToolDerived). The descriptor used to be committed as one
// aggregate file, gen/forge_descriptor.json, holding every service and method
// of the project (1.2 MB in reliant). Any two pull requests that touched any
// proto both rewrote it, and plain `git merge` — what GitHub runs
// server-side, where no merge driver runs — conflicted on it: the largest
// single source of merge conflicts in reliant and control-plane.
//
// So every reader goes through LoadDescriptor, which extracts the descriptor
// from the protos when it is missing or stale (the forge binary registers the
// extractor: RegisterDescriptorBuilder). `forge generate` writes it too, and
// deletes the legacy aggregate file. It is split per proto so an extraction
// rewrites only the files whose protos changed, and so a project that commits
// it anyway still merges independent proto changes cleanly.
//
// One thing the committed stamp used to answer that a cache cannot: whether
// the committed gen/ tree was generated from the protos on disk (`forge
// build`'s auto-generate). That is the GeneratedFromFile stamp, which only the
// generate pipeline writes, so an on-demand extraction never makes a stale
// gen/ look current.

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// DescriptorDir is the descriptor's project-relative directory.
const DescriptorDir = "gen/forge_descriptor"

// GeneratedFromFile records the proto stamps (DescriptorSources) of the last
// `forge generate` that wrote gen/, inside the ignored descriptor directory.
// The leading dot keeps it out of the descriptor's own files.
const GeneratedFromFile = DescriptorDir + "/.generated-from"

// descriptorBuilder extracts the descriptor from the protos into
// DescriptorDir. Set by the forge binary (RegisterDescriptorBuilder); nil in a
// test binary that does not link the extractor, where a missing descriptor
// reads as absent and a stale one is refused.
var descriptorBuilder func(projectDir string) error

// RegisterDescriptorBuilder installs the extractor LoadDescriptor runs when the
// descriptor is missing or stale. The extractor lives with the protoc plugin
// (internal/cli), which imports this package, hence the registration.
func RegisterDescriptorBuilder(build func(projectDir string) error) {
	descriptorBuilder = build
}

// WriteGeneratedFrom stamps the protos gen/ was just generated from. Only the
// generate pipeline calls it.
func WriteGeneratedFrom(projectDir string) error {
	body, err := json.MarshalIndent(DescriptorSources(projectDir), "", "  ")
	if err != nil {
		return err
	}
	file := filepath.Join(projectDir, filepath.FromSlash(GeneratedFromFile))
	if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
		return err
	}
	return atomicfile.WriteFile(file, append(body, '\n'), 0o644)
}

// ReadGeneratedFrom returns the stamp WriteGeneratedFrom wrote, or nil when
// this checkout has not generated (a fresh clone, whose committed gen/ the
// merge gate already proved current).
func ReadGeneratedFrom(projectDir string) map[string]string {
	data, err := os.ReadFile(filepath.Join(projectDir, filepath.FromSlash(GeneratedFromFile)))
	if err != nil {
		return nil
	}
	var sources map[string]string
	if json.Unmarshal(data, &sources) != nil {
		return nil
	}
	return sources
}

// LegacyDescriptorFile is the single aggregate file forge wrote before the
// descriptor was split per proto. `forge generate` deletes it.
const LegacyDescriptorFile = "gen/forge_descriptor.json"

// DescriptorFileFor is the project-relative descriptor file for one proto,
// given its proto-root-relative path: services/a/v1/a.proto →
// gen/forge_descriptor/services/a/v1/a.json.
func DescriptorFileFor(protoRel string) string {
	return path.Join(DescriptorDir, strings.TrimSuffix(filepath.ToSlash(protoRel), ".proto")+".json")
}

// DescriptorPresent reports whether the project has a descriptor directory.
func DescriptorPresent(projectDir string) bool {
	fi, err := os.Stat(filepath.Join(projectDir, DescriptorDir))
	return err == nil && fi.IsDir()
}

// DescriptorPart is one proto file's share of the descriptor.
type DescriptorPart struct {
	Services []ServiceDef
	Configs  []ConfigMessage
}

// ReadDescriptor reads the descriptor directory into one ForgeDescriptor,
// files in path order, WITHOUT checking it against the protos on disk
// (LoadDescriptor does that). Returns (nil, nil) when the project has none.
//
// Sources is the union of every file's stamp. It stays nil only when every
// file is unstamped (hand-built fixtures), and is `{}` for a directory with no
// files, which is what forge writes for a tree with no protos.
func ReadDescriptor(projectDir string) (*ForgeDescriptor, error) {
	root := filepath.Join(projectDir, DescriptorDir)
	if !DescriptorPresent(projectDir) {
		return nil, nil
	}
	var files []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && filepath.Ext(p) == ".json" {
			files = append(files, p)
		}
		return nil
	})
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", DescriptorDir, err)
	}
	sort.Strings(files)

	var desc ForgeDescriptor
	for _, f := range files {
		data, err := os.ReadFile(f)
		if err != nil {
			return nil, fmt.Errorf("read forge descriptor: %w", err)
		}
		var part ForgeDescriptor
		if err := json.Unmarshal(data, &part); err != nil {
			rel, _ := filepath.Rel(projectDir, f)
			return nil, fmt.Errorf("parse forge descriptor %s: %w", filepath.ToSlash(rel), err)
		}
		desc.Services = append(desc.Services, part.Services...)
		desc.Configs = append(desc.Configs, part.Configs...)
		if part.Sources != nil {
			if desc.Sources == nil {
				desc.Sources = map[string]string{}
			}
			for k, v := range part.Sources {
				desc.Sources[k] = v
			}
		}
	}
	if len(files) == 0 {
		desc.Sources = map[string]string{}
	}
	return &desc, nil
}

// WriteDescriptor writes the descriptor directory: one file per proto, from
// parts (keyed by proto-root-relative path) and the source stamps of the
// protos on disk. A stamped proto with no part still gets a file holding only
// its stamp, so the union of the files' stamps is exactly
// DescriptorSources(projectDir) and a deleted or added proto reads as drift.
// Files whose bytes are unchanged are not rewritten; files for protos that no
// longer exist are removed, and so is the legacy aggregate file.
func WriteDescriptor(projectDir string, parts map[string]DescriptorPart) error {
	sources := DescriptorSources(projectDir)
	keys := map[string]bool{}
	for rel := range parts {
		keys[rel] = true
	}
	for rel := range sources {
		keys[rel] = true
	}

	want := map[string][]byte{}
	for rel := range keys {
		part := parts[rel]
		fd := ForgeDescriptor{
			Services: part.Services,
			Configs:  part.Configs,
			Sources:  map[string]string{},
		}
		if fd.Services == nil {
			fd.Services = []ServiceDef{}
		}
		if fd.Configs == nil {
			fd.Configs = []ConfigMessage{}
		}
		if sum, ok := sources[rel]; ok {
			fd.Sources[rel] = sum
		}
		body, err := json.MarshalIndent(fd, "", "  ")
		if err != nil {
			return err
		}
		// A text file ends in a newline; without one every end-of-file fixer
		// rewrites the generated file and the next generate rewrites it back.
		want[filepath.Join(projectDir, filepath.FromSlash(DescriptorFileFor(rel)))] = append(body, '\n')
	}

	root := filepath.Join(projectDir, DescriptorDir)
	if err := os.MkdirAll(root, 0o755); err != nil {
		return fmt.Errorf("create %s: %w", DescriptorDir, err)
	}
	for file, body := range want {
		if old, err := os.ReadFile(file); err == nil && bytes.Equal(old, body) {
			continue
		}
		if err := os.MkdirAll(filepath.Dir(file), 0o755); err != nil {
			return err
		}
		if err := atomicfile.WriteFile(file, body, 0o644); err != nil {
			return fmt.Errorf("write forge descriptor: %w", err)
		}
	}
	if err := pruneDescriptorDir(root, want); err != nil {
		return err
	}
	if err := os.Remove(filepath.Join(projectDir, LegacyDescriptorFile)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return fmt.Errorf("remove %s: %w", LegacyDescriptorFile, err)
	}
	return nil
}

// pruneDescriptorDir removes every file under root that keep does not name
// (except dotfiles, GeneratedFromFile among them), then every directory left
// empty (deepest first), keeping root itself.
func pruneDescriptorDir(root string, keep map[string][]byte) error {
	var dirs []string
	err := filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if d.IsDir() {
			if p != root {
				dirs = append(dirs, p)
			}
			return nil
		}
		if _, ok := keep[p]; !ok && !strings.HasPrefix(d.Name(), ".") {
			return os.Remove(p)
		}
		return nil
	})
	if err != nil {
		return fmt.Errorf("prune %s: %w", DescriptorDir, err)
	}
	sort.Sort(sort.Reverse(sort.StringSlice(dirs)))
	for _, d := range dirs {
		if entries, err := os.ReadDir(d); err == nil && len(entries) == 0 {
			_ = os.Remove(d)
		}
	}
	return nil
}
