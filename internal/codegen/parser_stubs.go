package codegen

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io/fs"
	"log"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/assets"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/shadowdb"
)

// ForgeDescriptor is the JSON structure written by protoc-gen-forge --mode=descriptor.
type ForgeDescriptor struct {
	Services []ServiceDef    `json:"services"`
	Configs  []ConfigMessage `json:"configs"`

	// Sources fingerprints the .proto files this descriptor was extracted
	// from: proto-root-relative path → sha256 of the file's content. It exists
	// because the descriptor is a DERIVED CACHE of state that already lives in
	// the protos, and a cache that cannot detect its own staleness does not
	// degrade — it LIES.
	//
	// Measured: renaming one RPC in the descriptor, touching no .proto, made
	// `forge project graph` report an RPC that existed in no proto file and
	// omit one that existed in six places. Nothing warned, because loading
	// was read-file-and-unmarshal with no validation of any kind.
	//
	// One entry per file, not one digest of the tree: see DescriptorSources
	// for why that is what makes the stamp survive a merge.
	//
	// nil (the key absent, or null) means "not stamped by forge" — a
	// hand-built fixture — and skips the check. A forge-written descriptor
	// always carries the key, `{}` when it has no inputs.
	Sources map[string]string `json:"sources"`
}

// DescriptorSources fingerprints the protos a descriptor is extracted from:
// every .proto under <projectDir>/proto, keyed by its proto-root-relative
// path, valued by the sha256 of its content. Never nil, so a descriptor
// stamped from a tree with no inputs still reads as stamped.
//
// Content, not mtime: a checkout, a rebase or a `cp -R` rewrites mtimes
// without changing meaning, and would invalidate a descriptor that is
// perfectly good.
//
// ONE ENTRY PER FILE, because the stamp is committed (gen/ is checked in) and
// must merge. It used to be a single digest of the whole tree, and a digest of
// N files is a value no merge can produce: two branches that each edit a
// different proto each stamp a different digest, git keeps one of them, and
// the merged tree matches neither. Every merge that brought in a proto change
// made every open branch's descriptor stale, and main went stale whenever two
// such branches landed without a regenerate between them, while the real
// generated code (the .pb.go files, one per proto) merged correctly. Per-file
// entries change exactly where their inputs change, so two branches touching
// different protos merge to the stamp of the merged tree; the merge driver
// (mergeDescriptorSources) settles them key by key even when git would call
// two neighbouring lines a conflict.
//
// forge's vendored annotation protos (forge/v1/forge.proto,
// buf/validate/validate.proto) are not stamped. They are the GRAMMAR the
// protos are written in, not content any descriptor is extracted from, and
// they only change when `forge generate` itself re-vendors them, which
// regenerates everything in the same run. Stamping them is what made forge's
// own repository churn: its one proto is forge.proto, which most feature
// changes edit, and its descriptor (no services, no configs) was restamped by
// every one of them.
func DescriptorSources(projectDir string) map[string]string {
	sources := map[string]string{}
	root := filepath.Join(projectDir, "proto")
	_ = filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
		if err != nil || d.IsDir() || filepath.Ext(path) != ".proto" {
			return nil //nolint:nilerr // an unreadable tree contributes nothing
		}
		rel, relErr := filepath.Rel(root, path)
		if relErr != nil {
			return nil //nolint:nilerr // same
		}
		rel = filepath.ToSlash(rel)
		if isVendoredGrammar(rel) {
			return nil
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil //nolint:nilerr // same
		}
		sum := sha256.Sum256(data)
		sources[rel] = hex.EncodeToString(sum[:])
		return nil
	})
	return sources
}

// isVendoredGrammar reports whether rel (relative to proto/) is one of the
// annotation protos forge vendors into a project. See DescriptorSources.
func isVendoredGrammar(rel string) bool {
	for _, vendored := range []string{assets.ForgeProtoVendorRelPath, assets.ValidateProtoVendorRelPath} {
		if rel == strings.TrimPrefix(vendored, "proto/") {
			return true
		}
	}
	return false
}

// DescriptorDrift reports how the protos on disk differ from the ones a
// descriptor stamped with sources was extracted from — a proto edited, added
// or deleted since — or "" when they do not. Unstamped (nil) sources are
// never drift: see ForgeDescriptor.Sources.
func DescriptorDrift(projectDir string, sources map[string]string) string {
	if sources == nil {
		return ""
	}
	current := DescriptorSources(projectDir)
	var changed []string
	for rel, sum := range current {
		switch was, ok := sources[rel]; {
		case !ok:
			changed = append(changed, "proto/"+rel+" (added)")
		case was != sum:
			changed = append(changed, "proto/"+rel+" (edited)")
		}
	}
	for rel := range sources {
		if _, ok := current[rel]; !ok {
			changed = append(changed, "proto/"+rel+" (deleted)")
		}
	}
	if len(changed) == 0 {
		return ""
	}
	sort.Strings(changed)
	const shown = 3
	if len(changed) > shown {
		changed = append(changed[:shown], fmt.Sprintf("and %d more", len(changed)-shown))
	}
	return strings.Join(changed, ", ")
}

// noticeMissingDescriptor reports the absent gen/forge_descriptor.json —
// but ONLY for a project that actually declares a service in a .proto.
//
// A missing descriptor is a normal, permanent state for a project with no
// service protos (a CLI, a library): the pipeline skips descriptor
// extraction when features.codegen is off or nothing declares a service,
// so "run 'forge generate' with protoc-gen-forge" names a step that will
// never produce the file. Printing it unconditionally made an
// unactionable line the most frequent output of a healthy tree — forge's
// own repo emitted it two to three times per command. Where the project
// DOES declare a service, the descriptor is genuinely pending and the
// notice is real.
func noticeMissingDescriptor(projectDir string) {
	if !protoSourceDeclaresService(projectDir) {
		return
	}
	log.Println("Info: forge_descriptor.json not found — run 'forge generate' with protoc-gen-forge to produce it")
}

// protoSourceDeclaresService reports whether any .proto under
// <projectDir>/proto declares a service. Cheap line scan, matching the
// pipeline's own gate (generate_helpers.go's protoSourceHasService):
// the first token on a line must be the `service` keyword, so prose in
// doc comments and `service`-suffixed message names do not match.
func protoSourceDeclaresService(projectDir string) bool {
	found := false
	_ = filepath.Walk(filepath.Join(projectDir, "proto"), func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() || filepath.Ext(path) != ".proto" {
			return nil //nolint:nilerr // an unreadable tree simply declares no service
		}
		data, readErr := os.ReadFile(path)
		if readErr != nil {
			return nil
		}
		for _, line := range strings.Split(string(data), "\n") {
			if fields := strings.Fields(line); len(fields) > 0 && fields[0] == "service" {
				found = true
				return filepath.SkipAll
			}
		}
		return nil
	})
	return found
}

// loadDescriptor reads and parses the forge_descriptor.json file from the gen/ directory.
func loadDescriptor(projectDir string) (*ForgeDescriptor, error) {
	descPath := filepath.Join(projectDir, "gen", "forge_descriptor.json")
	data, err := os.ReadFile(descPath)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil // No descriptor yet — not an error
		}
		return nil, fmt.Errorf("read forge descriptor: %w", err)
	}

	var desc ForgeDescriptor
	if err := json.Unmarshal(data, &desc); err != nil {
		return nil, fmt.Errorf("parse forge descriptor: %w", err)
	}

	// REFUSE a descriptor that does not match the protos on disk. Warning
	// would not do: every caller here feeds codegen or an inspection command,
	// and each would carry on emitting confident, wrong answers from a cache
	// nobody re-derived. A hard error names the one command that fixes it.
	//
	// Only a STAMPED descriptor is checked. An unstamped one is a hand-built
	// fixture, and refusing those would break every caller that constructs a
	// descriptor directly without a proto tree to hash. The drift this
	// guards against is a stamped descriptor whose protos then changed —
	// which is the sequence that actually happens.
	if drift := DescriptorDrift(projectDir, desc.Sources); drift != "" {
		return nil, fmt.Errorf(
			"gen/forge_descriptor.json is stale: it was extracted from different .proto files than the ones on disk (%s).\n"+
				"  It is a DERIVED cache — the protos are the source of truth — so forge refuses to answer from it.\n"+
				"  Fix: reliant forge generate", drift)
	}
	return &desc, nil
}

// ParseServicesFromProtos reads the services forge MANAGES from the forge
// descriptor: every declared service except those marked
// `// forge:unmanaged`. Falls back to empty if the descriptor does not exist
// yet.
//
// This is the set every service-scoped consumer reads — handler scaffolds,
// CRUD, mocks, composition and mounts, cmd subcommands, frontend hooks and
// pages, audit, doctor, scaffold. Filtering HERE rather than at each consumer
// is the point: a consumer that forgot to filter would scaffold stubs for
// handlers that live elsewhere, which is the failure the marker exists to
// prevent. A consumer that genuinely needs the whole wire contract asks for
// it by name with ParseDeclaredServices.
func ParseServicesFromProtos(dir string, projectDir string) ([]ServiceDef, error) {
	defs, err := ParseDeclaredServices(projectDir)
	if err != nil {
		return nil, err
	}
	return ManagedServices(defs), nil
}

// ParseDeclaredServices reads EVERY service the protos declare, managed or
// not, with ModulePath set from go.mod. Falls back to empty if the
// descriptor does not exist yet.
//
// Only for consumers that project the API contract rather than forge's
// handler model — the open-procedure set (auth_required describes the rpc,
// whoever implements it), "does this project declare services at all", and
// the unmanaged-service listing in `forge project audit`. Everything else
// reads ParseServicesFromProtos.
func ParseDeclaredServices(projectDir string) ([]ServiceDef, error) {
	desc, err := loadDescriptor(projectDir)
	if err != nil {
		return nil, err
	}
	if desc == nil {
		noticeMissingDescriptor(projectDir)
		return nil, nil
	}

	// Set ModulePath on each service from the project's go.mod
	modulePath, modErr := GetModulePath(projectDir)
	if modErr == nil {
		for i := range desc.Services {
			desc.Services[i].ModulePath = modulePath
		}
	}

	return desc.Services, nil
}

// ManagedServices returns the services in defs that forge manages — those
// without a `// forge:unmanaged` marker — in their original order.
func ManagedServices(defs []ServiceDef) []ServiceDef {
	var out []ServiceDef
	for _, d := range defs {
		if !d.Unmanaged {
			out = append(out, d)
		}
	}
	return out
}

// UnmanagedServices returns the services in defs marked `// forge:unmanaged`,
// in their original order.
func UnmanagedServices(defs []ServiceDef) []ServiceDef {
	var out []ServiceDef
	for _, d := range defs {
		if d.Unmanaged {
			out = append(out, d)
		}
	}
	return out
}

// ParseEntityProtos returns the project's entities. Despite the
// historical name, entities are no longer parsed from proto
// annotations: they are the join of the APPLIED schema (db/migrations
// shadow-applied + introspected) with the service protos' CRUD method
// shapes — see BuildSchemaEntities. Falls back to empty when the
// descriptor or migrations don't exist yet.
//
// Only MANAGED services' CRUD rpcs make an entity. A CRUD-shaped rpc on a
// `// forge:unmanaged` service is hand-implemented elsewhere; projecting an
// entity from it would wire generic CRUD, pages and seeds over business
// logic forge does not own. The entity appears when the service is adopted.
func ParseEntityProtos(projectDir string) ([]EntityDef, error) {
	desc, err := loadDescriptor(projectDir)
	if err != nil {
		return nil, err
	}
	if desc == nil {
		noticeMissingDescriptor(projectDir)
		return nil, nil
	}
	return BuildSchemaEntities(projectDir, ManagedServices(desc.Services))
}

// ParseORMEntities returns the data layer's entities: every applied BASE
// TABLE (see BuildORMEntities), with the CRUD join's name and wire markers
// where a managed service claims the table. Unlike ParseEntityProtos it
// needs no descriptor — a project whose protos have not been compiled yet
// still has a schema, and the store is a projection of the schema alone.
func ParseORMEntities(projectDir string) ([]EntityDef, error) {
	desc, err := loadDescriptor(projectDir)
	if err != nil {
		return nil, err
	}
	var services []ServiceDef
	if desc != nil {
		services = ManagedServices(desc.Services)
	}
	return BuildORMEntities(projectDir, services)
}

// ParseUnmatchedCRUDRPCs lists the managed services' CRUD-named rpcs whose
// entity has a wire message but no table (UnmatchedCRUDRPCs) and that no
// handler implements by hand (WithoutHandImplemented). Empty when the
// descriptor or the migrations do not exist yet.
func ParseUnmatchedCRUDRPCs(projectDir string) ([]UnmatchedCRUDRPC, error) {
	desc, err := loadDescriptor(projectDir)
	if err != nil || desc == nil {
		return nil, err
	}
	tables, err := shadowdb.Tables(projectDir, filepath.Join(projectDir, "db", "migrations"))
	if err != nil {
		return nil, err
	}
	return WithoutHandImplemented(projectDir, UnmatchedCRUDRPCs(tables, ManagedServices(desc.Services))), nil
}

// ParseConfigProto reads config messages from the forge descriptor,
// filtering to those from a specific proto file path.
func ParseConfigProto(protoPath string) ([]ConfigMessage, error) {
	// Infer project dir from the proto path
	projectDir := protoPath
	for {
		parent := filepath.Dir(projectDir)
		if parent == projectDir {
			break
		}
		projectDir = parent
		if _, err := os.Stat(filepath.Join(projectDir, "go.mod")); err == nil {
			break
		}
	}

	desc, err := loadDescriptor(projectDir)
	if err != nil {
		return nil, err
	}
	if desc == nil {
		return nil, nil
	}
	return desc.Configs, nil
}

// ParseConfigProtosFromDir reads all config messages from the forge descriptor.
// Falls back to empty if the descriptor does not exist yet.
func ParseConfigProtosFromDir(dir string) ([]ConfigMessage, error) {
	// Walk up from dir to find the project root (contains go.mod)
	projectDir := dir
	for {
		if _, err := os.Stat(filepath.Join(projectDir, "go.mod")); err == nil {
			break
		}
		parent := filepath.Dir(projectDir)
		if parent == projectDir {
			// Couldn't find go.mod — use the original dir
			projectDir = dir
			break
		}
		projectDir = parent
	}

	desc, err := loadDescriptor(projectDir)
	if err != nil {
		return nil, err
	}
	if desc == nil {
		noticeMissingDescriptor(projectDir)
		return nil, nil
	}
	return desc.Configs, nil
}

// GetModulePath reads the module path from go.mod in the given directory.
func GetModulePath(dir string) (string, error) {
	data, err := os.ReadFile(filepath.Join(dir, "go.mod"))
	if err != nil {
		return "", err
	}

	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimSpace(line), "module ") {
			return strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(line), "module ")), nil
		}
	}

	return "", fmt.Errorf("module directive not found in go.mod")
}

// projectAPIRESTEnabled reports whether the project at projectDir declares
// `api.rest: true`. The bootstrap generator uses it to wrap the Connect mux
// with a vanguard REST transcoder, and the CRUD-gen pass uses it to emit
// `google.api.http` annotations on standard CRUD RPCs.
//
// Best-effort by design: an unreadable or invalid forge.yaml resolves to
// false (no REST) so this is safe to call from any project shape, including
// the initial scaffold pass before forge.yaml grows an `api:` block. It goes
// through the canonical loader rather than scanning lines, so every YAML
// spelling of the key — flow style, quoted scalar, anchor — reads the same
// as it does everywhere else in forge.
func projectAPIRESTEnabled(projectDir string) bool {
	cfg, err := config.LoadProjectDir(projectDir)
	if err != nil {
		return false
	}
	return cfg.API.REST
}
