package generator

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/naming"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// ScaffoldMode controls how scaffolding functions handle pre-existing output
// files. It exists so `forge scaffold service --resume / --force` can re-run a
// partial scaffold without forcing the user to rm -rf and start over.
type ScaffoldMode int

const (
	// ScaffoldFail is the default: writes unconditionally. Callers are
	// expected to have done their own up-front "does this name conflict"
	// check (forge.yaml services: list). Equivalent to pre-resume behavior.
	ScaffoldFail ScaffoldMode = iota
	// ScaffoldResume skips any output file that already exists on disk.
	// Use this to recover from a partial scaffold where buf generate (or a
	// later pipeline step) failed mid-flight and left some files written.
	ScaffoldResume
	// ScaffoldForce overwrites every output file, even when present. The
	// proto file is included — the only file the default codepath
	// historically preserved — so users can fully re-stamp a scaffold.
	ScaffoldForce
)

// GenerateServiceFiles generates all files for a single Go service:
//   - handlers/<servicePackage>/service.go (from service/service.go.tmpl)
//   - proto/services/<servicePackage>/v1/<servicePackage>.proto (inline stub, skipped if exists)
//
// The display/CLI form of the service name (which may contain hyphens) is
// translated to a Go-package-safe form ("admin-server" -> "admin_server") for
// filesystem directories and Go/proto identifiers. Display strings keep the
// original spelling.
//
// Both the "new project" and "scaffold service" flows delegate here so the
// generated output is always identical.
//
// No handler file is emitted at scaffold time: with zero RPC methods there
// is nothing for one to contain. Once RPCs are added to the proto file,
// `forge generate` scaffolds each missing stub into its own user-owned
// rpc_<name>.go (codegen.GenerateMissingHandlerStubs).
//
// There is no per-service port: every service mounts onto the one Connect
// mux the binary serves, and the listener reads the AppConfig `port` field
// (env PORT, default 8080) set per environment in deploy/kcl/<env>/config.k.
func GenerateServiceFiles(root, modulePath, serviceName, projectName string) error {
	return GenerateServiceFilesWithMode(root, modulePath, serviceName, projectName, ScaffoldFail, nil)
}

// GenerateServiceFilesWithMode is the mode-aware form of GenerateServiceFiles.
// `mode` controls per-file overwrite/skip behavior so callers can support
// --resume (recover a partially-applied scaffold) and --force (re-stamp
// every output file). When non-nil, `progress` receives one human-readable
// line per scaffolding step so the CLI can render "skipped" / "overwriting"
// notices without re-implementing the existence check.
func GenerateServiceFilesWithMode(root, modulePath, serviceName, projectName string, mode ScaffoldMode, progress io.Writer) error {
	servicePackage := naming.ServicePackage(serviceName)
	svcDir := filepath.Join(root, "internal", "handlers", servicePackage)

	// Create directories
	dirs := []string{
		svcDir,
		filepath.Join(root, "proto", "services", servicePackage, "v1"),
	}
	for _, d := range dirs {
		if err := os.MkdirAll(d, 0755); err != nil {
			return fmt.Errorf("create directory %s: %w", d, err)
		}
	}

	// The proto below declares `service <handlerName>Service`; service.go
	// embeds and constructs the handler protoc-gen-connect-go generates for
	// it, which is spelled from the service's GoName, not the proto name
	// ("alphav1connect" → Alphav1connectService → Alphav1ConnectServiceHandler).
	handlerName := naming.ToPascalCase(serviceName)
	connectService := naming.ConnectServiceGoName(handlerName + "Service")

	// -- service.go (via service/service.go.tmpl) --
	svcData := struct {
		ServiceName         string
		ServicePackage      string
		Module              string
		ProtoImportPath     string
		ProtoConnectPackage string
		ProtoFileSymbol     string
		HandlerName         string
		Methods             []string
		HasConfig           bool
	}{
		ServiceName:         serviceName,
		ServicePackage:      servicePackage,
		Module:              modulePath,
		ProtoImportPath:     fmt.Sprintf("services/%s", servicePackage),
		ProtoConnectPackage: fmt.Sprintf("%sv1connect", servicePackage),
		ProtoFileSymbol:     fmt.Sprintf("File_services_%s_v1_%s_proto", servicePackage, servicePackage),
		HandlerName:         connectService,
		Methods:             []string{},
		HasConfig:           codegen.ProjectHasTypedConfig(root),
	}

	svcPath := filepath.Join(svcDir, "service.go")
	if err := renderAndWriteWithMode(svcPath, "service/service.go.tmpl", svcData, mode, progress); err != nil {
		return err
	}

	// No handler file is emitted at scaffold (zero RPC methods). See the
	// function docstring for details.

	// -- test templates --
	// None here, deliberately. The scaffold test is ONE FILE PER RPC
	// (handlers_scaffold_<rpc>_test.go), and this path runs before any RPC
	// exists, so there is nothing to write. Each file is born alongside its
	// RPC's Unimplemented stub, in GenerateMissingHandlerStubs.
	//
	// What used to be here was a file containing only a
	// `forge-unit-test-placeholder` comment, plus a rule elsewhere that
	// forge may rewrite the file while that comment survives. That made a
	// comment string in a user-owned file decide whether forge would
	// overwrite it — behavior you cannot trace to a call site. Absence is
	// now the whole ownership rule: no file, no RPC; a file that exists is
	// yours.
	//
	// No one-shot integration_test.go scaffold either: the unit scaffold
	// owns per-RPC self-destructing rows and handlers_crud_test.go owns the
	// DB-bound CRUD surface. A second per-RPC file that hardcodes
	// WantErr for every method goes stale the moment handlers are
	// implemented and teaches green-means-nothing.

	// -- proto stub --
	// Historical behavior preserved the proto file even in ScaffoldFail mode
	// (the proto file is the user's API contract — clobbering it would erase
	// hand-written RPC definitions). ScaffoldForce overrides that guard for
	// users who explicitly asked to re-stamp the scaffold.
	protoPath := filepath.Join(root, "proto", "services", servicePackage, "v1", fmt.Sprintf("%s.proto", servicePackage))
	// The stub carries the (forge.v1.service) option block from birth. The
	// forge/v1/forge.proto import is load-bearing for the option extension;
	// emitting it now (rather than letting `forge scaffold entity`
	// back-fill it) keeps a fresh service buf-valid on its own.
	//
	// The stub carries no access-control annotations: access control is
	// app-owned handler logic.
	//
	// The proto PACKAGE follows the project (forge.yaml api.proto_package,
	// else the convention its existing service protos share, else
	// services.<svc>.v1 — see ResolveServiceProtoPackage). go_package does
	// NOT follow it: every service keeps its own gen/services/<svc>/v1 Go
	// package, which is where buf's paths=source_relative writes the stubs
	// and where the handler template imports them from, whether or not the
	// wire package is shared.
	protoPkg := ResolveServiceProtoPackage(root, serviceName)
	if protoPkg.Source != ProtoPackageDefault {
		fmt.Fprintf(progressOrDiscard(progress), "  ✓ %s\n", protoPkg.Describe())
	}
	protoContent := fmt.Sprintf(`syntax = "proto3";

package %s;

import "forge/v1/forge.proto";

option go_package = "%s/gen/services/%s/v1;%sv1";

// %sService defines the %s service RPCs.
service %sService {
  option (forge.v1.service) = {
    name: "%sService"
    version: "1.0.0"
    description: "%s service"
  };

  // TODO: Add your RPC methods here.
}
`, protoPkg.Package, modulePath, servicePackage, servicePackage,
		handlerName, serviceName, handlerName, handlerName, serviceName)
	if err := writeBytesWithMode(protoPath, []byte(protoContent), mode, progress); err != nil {
		return err
	}

	// Read the actual proto after --resume/preservation, not the inferred
	// candidate: an existing user-owned proto may declare another package.
	return reconcileSharedPackageBuf(root, protoPath, progress)
}

// renderAndWriteWithMode renders a service template then delegates the
// write decision to writeBytesWithMode.
func renderAndWriteWithMode(path, tmplName string, data any, mode ScaffoldMode, progress io.Writer) error {
	// Honor skip BEFORE rendering — rendering some templates depends on
	// codegen helpers that walk the project tree, so skipping early keeps
	// --resume cheap.
	if mode == ScaffoldResume {
		if _, err := os.Stat(path); err == nil {
			emitProgress(progress, "skipped", path)
			return nil
		}
	}
	content, err := renderServiceTemplate(tmplName, data)
	if err != nil {
		return fmt.Errorf("render %s: %w", tmplName, err)
	}
	return writeBytesWithMode(path, content, mode, progress)
}

// writeBytesWithMode writes `content` to `path` honoring the scaffold mode:
//   - ScaffoldFail: always write (preserves pre-resume default semantics).
//     The one historical exception is the proto file; callers that need to
//     preserve it pass ScaffoldFail and inspect existence themselves, but
//     this helper centralizes the existence check so the proto path can
//     simply call through with the default mode and rely on protected
//     handling here.
//   - ScaffoldResume: skip if path exists.
//   - ScaffoldForce: always write; emit an "overwriting" notice when the
//     file already exists.
//
// In ScaffoldFail mode we preserve an existing proto file at the only
// historically-protected location by skipping when the file exists AND
// looks like a proto stub the user may have edited. Other files clobber
// as before.
func writeBytesWithMode(path string, content []byte, mode ScaffoldMode, progress io.Writer) error {
	exists := false
	if _, err := os.Stat(path); err == nil {
		exists = true
	}
	switch mode {
	case ScaffoldResume:
		if exists {
			emitProgress(progress, "skipped", path)
			return nil
		}
	case ScaffoldForce:
		if exists {
			emitProgress(progress, "overwriting", path)
		}
	case ScaffoldFail:
		// Preserve the historical "don't clobber an existing proto file"
		// guard. Detect by suffix to keep the rule simple and visible.
		if exists && strings.HasSuffix(path, ".proto") {
			return nil
		}
	}
	return atomicfile.WriteFile(path, content, 0644)
}

// progressOrDiscard lets a nil progress writer be passed straight to Fprintf.
func progressOrDiscard(w io.Writer) io.Writer {
	if w == nil {
		return io.Discard
	}
	return w
}

// emitProgress writes a single human-readable status line to `w` if non-nil.
// The CLI uses this to surface --resume / --force decisions to the user.
func emitProgress(w io.Writer, kind, path string) {
	if w == nil {
		return
	}
	switch kind {
	case "skipped":
		fmt.Fprintf(w, "  ✓ skipped: %s\n", path)
	case "overwriting":
		fmt.Fprintf(w, "  ⚠ overwriting: %s\n", path)
	}
}
