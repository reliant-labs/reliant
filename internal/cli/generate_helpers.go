package cli

import (
	"bufio"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/codegen"
)

func dirExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.IsDir()
}

func fileExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return !info.IsDir()
}

func isPluginAvailable(pluginName string) bool {
	_, err := exec.LookPath(pluginName)
	return err == nil
}

// discoverProtoSubdirs returns project-relative paths of every immediate
// subdir under proto/ that contains at least one .proto file. The result
// is sorted for determinism.
//
// This unblocks scaffolded protos that live outside the canonical
// proto/{services,api,db,config} layout (e.g. proto/audit/,
// proto/api_key/). Without this,
// the descriptor walk and frontend TS generation silently drop those
// services, which surfaces as "missing useListAuditEvents hook" downstream.
func discoverProtoSubdirs(projectDir string) []string {
	root := filepath.Join(projectDir, "proto")
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil
	}
	var dirs []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		// proto/buf holds the vendored protovalidate option definitions — a
		// COMPILE-only import buf resolves via the module path. It must NOT
		// be a codegen TARGET (descriptor/TS --path), so it is skipped here;
		// the Go buf.gen.yaml excludes it via exclude_paths.
		if e.Name() == "buf" {
			continue
		}
		sub := filepath.Join("proto", e.Name())
		has, err := hasProtoFilesInDir(filepath.Join(projectDir, sub))
		if err != nil || !has {
			continue
		}
		dirs = append(dirs, sub)
	}
	sort.Strings(dirs)
	return dirs
}

// projectDefinesConnectServices reports whether the project declares at
// least one Connect RPC service in ANY proto package — regardless of
// whether the protos live under the canonical proto/services/<svc>/v1/
// layout or a flat/multi-service layout (e.g. control-plane's
// proto/controlplane/v1/*.proto). This is the descriptor-backed
// generalization of the historical `dirExists(proto/services)` check.
//
// Source-of-truth order:
//
//  1. gen/forge_descriptor.json — the protoc-gen-forge descriptor. It
//     enumerates every Connect service the buf pipeline saw, in any proto
//     package, so once it exists it is the authoritative answer. (This is
//     also exactly the set ParseServicesFromProtos returns, so HasServices
//     and ctx.Services stay in lockstep.)
//
//  2. proto-source scan — on a fresh tree the descriptor does not exist
//     yet when proto-dir detection runs (descriptor generation happens
//     LATER in the pipeline). Fall back to scanning every *.proto file
//     under proto/ for a top-level `service ` declaration. This keeps the
//     pre-descriptor gates (e.g. "parse services + module path") firing on
//     the first run so the descriptor is produced and then becomes the
//     authoritative source for the downstream gates.
//
// This signal is "does the project DECLARE services" — the project's
// shape — not "how many did the descriptor enumerate this instant".
// stepParseServicesAndModule deliberately does NOT downgrade HasServices
// when the descriptor enumeration comes back empty: descriptor generation
// is best-effort (warnOrFail), and flipping HasServices false off a
// transient empty enumeration would silently disable every service-codegen
// step — the silent-stomp failure the loud-by-default model forbids. The
// per-service emitters iterate ctx.Services, so an empty slice is a
// self-correcting no-op without disabling the steps wholesale.
//
// Unmanaged services count: this is the project's declared shape, and the
// open-procedure set still projects over them.
func projectDefinesConnectServices(projectDir string) bool {
	defs, err := codegen.ParseDeclaredServices(projectDir)
	if err == nil && len(defs) > 0 {
		return true
	}
	// Descriptor absent or empty (fresh tree, pre-descriptor-generation).
	// Scan the proto source for any `service` declaration in any package.
	return protoSourceHasService(projectDir)
}

// protoSourceHasService walks every *.proto under proto/ and reports
// whether any file declares a top-level Connect/gRPC service. The scan is
// deliberately layout-agnostic — it does not care which subdirectory the
// proto lives in — so flat (proto/controlplane/v1/*.proto) and
// per-service (proto/services/<svc>/v1/*.proto) layouts both register.
func protoSourceHasService(projectDir string) bool {
	root := filepath.Join(projectDir, "proto")
	found := false
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil
		}
		if filepath.Ext(path) != ".proto" {
			return nil
		}
		if protoFileDeclaresService(path) {
			found = true
			return filepath.SkipAll
		}
		return nil
	})
	return found
}

// protoFileDeclaresService reports whether a single .proto file has a
// `service <Name> {` declaration. We match on a line whose first token is
// the `service` keyword (after trimming leading whitespace) to avoid false
// positives from comments, the `// a service that …` prose in doc
// comments, or `service`-suffixed message names. Cheap line scan — no full
// proto parse — because this only gates pipeline steps; the descriptor is
// the authoritative enumeration.
func protoFileDeclaresService(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	// Read-only scan; a Close error on a file that was never written
	// cannot lose data.
	defer func() { _ = f.Close() }()
	sc := bufio.NewScanner(f)
	// Proto files can have long lines (e.g. inlined options); raise the
	// token cap so the scan does not silently stop at the default 64 KiB.
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if strings.HasPrefix(line, "//") || strings.HasPrefix(line, "*") {
			continue
		}
		// First token must be exactly `service` followed by whitespace —
		// "service Foo {" / "service Foo{". Rejects "services" / a field
		// named service and comment prose.
		const kw = "service"
		if !strings.HasPrefix(line, kw) {
			continue
		}
		rest := line[len(kw):]
		if rest == "" || rest[0] == ' ' || rest[0] == '\t' {
			return true
		}
	}
	return false
}
