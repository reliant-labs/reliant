// File: internal/cli/scaffold/rpc.go
//
// `forge scaffold rpc <svc> <Name>` scaffolds a single custom RPC
// into an existing service. CRUD codegen (crud_gen.go) covers the
// Create/Get/List/Update/Delete shapes; this command is for hand-written
// RPCs. Every RPC lives as a method on the handler package's `*Service`
// working with pb (wire) types — the "pb-through" shape. It has two
// modes:
//
//   - RPC already declared in the service proto: the pb-through handler
//     stub is exactly what the generate pipeline emits
//     (GenerateMissingHandlerStubs), into the same rpc_<snake>.go this
//     command writes, so the command just runs generate so the method
//     lands wired (a bare Unimplemented stub on *Service, or a CRUD shim
//     if the RPC is entity-backed).
//
//   - RPC not in the proto yet: write the correctly-signed handler stub
//     and print the proto snippet to paste (--stream picks the streaming
//     shape).
//
// Deliberate non-goals:
//
//   - We do NOT edit the .proto file. Proto files have hand-curated
//     section markers, ordering, and comments; an automated injector
//     would regress those constantly. Instead, we print the snippet so
//     the user can paste it into the right `service { ... }` block.
//
//   - We do NOT derive schema from RPC shapes. Migrations stay the single
//     owner of schema — an entity is born by marking its message
//     `// forge:entity` and running bare `forge scaffold`.

package scaffold

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/cli/factory"
	"github.com/reliant-labs/forge/internal/cliutil"
	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/generator"
	"github.com/reliant-labs/forge/internal/naming"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// rpcDeclaredInServiceProtoDir reports whether any .proto file under the
// service's proto directory (proto/services/<leaf>/...) declares rpc
// <name> — the raw-truth half of the stale-descriptor discriminator.
func rpcDeclaredInServiceProtoDir(root, svc, rpcName string) bool {
	dir := filepath.Join(root, "proto", "services", naming.ServicePackage(svc))
	re := rpcDeclRE(rpcName)
	found := false
	_ = filepath.WalkDir(dir, func(path string, d fs.DirEntry, err error) error {
		if err != nil || found || d.IsDir() || !strings.HasSuffix(d.Name(), ".proto") {
			return nil
		}
		if data, rerr := os.ReadFile(path); rerr == nil && re.Match(data) {
			found = true
		}
		return nil
	})
	return found
}

// rpcStreamMode is the parsed --stream flag. The zero value is "unary".
type rpcStreamMode int

const (
	rpcUnary rpcStreamMode = iota
	rpcServerStream
	rpcClientStream
	rpcBidiStream
)

// parseStreamMode maps the --stream flag value to the internal mode enum.
// Empty string == unary so the flag is genuinely optional; an explicit
// "unary" is also accepted because some users will type it for symmetry.
func parseStreamMode(s string) (rpcStreamMode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", "unary":
		return rpcUnary, nil
	case "server":
		return rpcServerStream, nil
	case "client":
		return rpcClientStream, nil
	case "bidi", "bidirectional", "both":
		return rpcBidiStream, nil
	default:
		return 0, fmt.Errorf("unknown stream mode %q: valid modes are unary (default), server, client, bidi", s)
	}
}

// newRPCCmd is the cobra surface for `forge scaffold rpc`.
func newRPCCmd(f *factory.Factory) *cobra.Command {
	var streamFlag string
	cmd := &cobra.Command{
		Use:   "rpc <svc> <Name>",
		Short: "Scaffold a custom RPC on the service's handler package",
		Long: `Scaffold a custom RPC onto an existing service.

Every RPC is a method on the handler package's *Service working with the
generated pb (wire) types — the "pb-through" shape.

Either way the stub lands in its own internal/handlers/<svc>/rpc_<name>.go
— one file per RPC, so two people implementing two RPCs of the same
service never touch the same file.

When the RPC already exists in the service proto (run 'forge generate'
after editing the proto), this runs the generate pipeline so the
pb-through handler stub lands there — a method on *Service returning
Unimplemented until you fill it in. (An entity-backed CRUD-shaped RPC is
wired as a CRUD shim in handlers_crud.go instead.)

When the RPC is NOT in the proto yet, a handler stub with the correct
Connect signature is written and the proto snippet is printed for you to
paste (--stream picks the streaming shape: server, client, bidi; omit
for unary). The proto edit is left to you because proto files have
hand-curated section markers and ordering that an automated injector
would regress.

Examples:
  forge scaffold rpc tasks ListTasksByOwner
  forge scaffold rpc events TailEvents --stream server
  forge scaffold rpc chat Chat --stream bidi`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			mode, err := parseStreamMode(streamFlag)
			if err != nil {
				return cliutil.UserErr("forge scaffold rpc", err.Error(), "",
					"pass --stream server, --stream client, --stream bidi, or omit for unary")
			}
			return runRPC(f, args[0], args[1], mode)
		},
	}
	cmd.Flags().StringVar(&streamFlag, "stream", "", "stream mode (server, client, bidi); omit for unary — only used when the RPC is not in the proto yet")
	return cmd
}

// runRPC validates inputs, then branches on whether the RPC is
// already declared in the service proto: run the generate pipeline (which
// emits the pb-through handler stub, or CRUD wiring for an entity-backed
// RPC) when it is, the stub + proto snippet when it isn't.
func runRPC(f *factory.Factory, svc, rpcName string, mode rpcStreamMode) error {
	ctxLabel := fmt.Sprintf("forge scaffold rpc %s %s", svc, rpcName)

	if err := validateIdentifier(svc); err != nil {
		return cliutil.WrapUserErr(ctxLabel, "invalid service name", "",
			"use a name starting with a letter, containing letters/digits/_/-", err)
	}
	if err := validateIdentifier(rpcName); err != nil {
		return cliutil.WrapUserErr(ctxLabel, "invalid RPC name", "",
			"use a PascalCase name starting with an uppercase letter (e.g. ListTasksByOwner)", err)
	}
	// The RPC name becomes a Go method on the service receiver, so it
	// must start uppercase. validateIdentifier accepts lower-case starts
	// for filenames/package names; we tighten the rule here.
	if !startsUpper(rpcName) {
		return cliutil.UserErr(ctxLabel,
			fmt.Sprintf("RPC name %q must start with an uppercase letter", rpcName),
			"",
			"PascalCase your name so it becomes an exported method (e.g. ListTasksByOwner, not listTasksByOwner)")
	}

	root, err := projectRoot()
	if err != nil {
		return err
	}
	if err := requireServiceKind(root, "rpc"); err != nil {
		return err
	}
	if err := refuseUnmanagedService(ctxLabel, root, svc); err != nil {
		return err
	}

	// Tombstoned services (mentioned only in a pkg/app/services.go
	// comment — types-only) have no handler scaffold by design: adding
	// an RPC handler here would contradict the registration file. An
	// UNLISTED (newly added, not yet registered) service still has a
	// scaffold and falls through — implementing before registering is a
	// supported flow. Best-effort parse: a broken registry falls
	// through to the handler-dir check below.
	if reg, regErr := f.Gen.LoadServiceRegistry(root); regErr == nil && reg.Tombstoned(svc) {
		return cliutil.UserErr(ctxLabel,
			fmt.Sprintf("service %q is types-only — %s deliberately does not register it (its row was deleted; see the comment there), so this binary has no handler scaffold to add an RPC to", svc, f.Gen.ServiceRegistryRelPath),
			f.Gen.ServiceRegistryRelPath,
			fmt.Sprintf("add the RPC to the proto only (the types/client still generate), implement it in the binary the %s comment names, or restore the `%s(app, cfg, logger, opts...),` row to serve it here", f.Gen.ServiceRegistryRelPath, codegen.ServiceRowFuncName(svc)))
	}

	// Disk-first handler resolution; legacy dir spellings (compact/kebab)
	// must keep working.
	hc, err := codegen.ResolveComponentDir(root, "internal/handlers", svc)
	if err != nil {
		// The dir exists but its package clause is undeterminable (empty
		// or broken package). The bare stub is still writable with the
		// synthesized package name.
		dir := filepath.Join(root, "internal", "handlers", naming.ServicePackage(svc))
		if fi, statErr := os.Stat(dir); statErr == nil && fi.IsDir() {
			pbPath, perr := pbImportPathFor(root, svc)
			if perr != nil {
				return perr
			}
			return writeBareRPCStub(ctxLabel, dir, naming.ServicePackage(svc), rpcName, pbPath, mode, true)
		}
		return err
	}
	if !hc.FromDisk {
		return cliutil.UserErr(ctxLabel,
			fmt.Sprintf("service %q has no handler directory at %s", svc, hc.Dir),
			"",
			fmt.Sprintf("run `forge scaffold service %s` first to scaffold the service", svc))
	}

	// ── the RPC is already declared in the proto ─────────────────────
	// The pb-through handler stub is exactly what the generate pipeline
	// emits (GenerateMissingHandlerStubs) — a method on *Service returning
	// Unimplemented, or a CRUD shim when the RPC is entity-backed. Run
	// generate so the method lands wired.
	if rpcDeclaredInServiceProtoDir(root, svc, rpcName) {
		fmt.Printf("🔧 %s is declared in the proto — running generate to emit the handler...\n", rpcName)
		if gerr := f.Gen.RunPipeline(root); gerr != nil {
			return cliutil.WrapUserErr(ctxLabel, "run generate", "",
				"fix the generate failure, then re-run this command", gerr)
		}
		fmt.Printf("✅ %s is wired on internal/handlers/%s/*Service (%s).\n",
			rpcName, hc.ImportLeaf, codegen.RPCHandlerFileName(rpcName))
		fmt.Println()
		fmt.Println("Next steps:")
		fmt.Println("  1. Fill in the handler body — the pb-through stub returns")
		fmt.Println("     Unimplemented (or, for an entity-backed RPC, delegates to the")
		fmt.Println("     generated CRUD ops) until you do.")
		fmt.Printf("  2. Iterate with `task test:short -- ./internal/handlers/%s/...`;\n", hc.ImportLeaf)
		fmt.Println("     run `task test` before you finish.")
		return nil
	}

	// ── the RPC is not in the proto yet ──────────────────────────────
	pbPath, perr := pbImportPathFor(root, svc)
	if perr != nil {
		return perr
	}
	return writeBareRPCStub(ctxLabel, hc.Dir, hc.PackageName, rpcName, pbPath, mode, true)
}

// writeBareRPCStub is the historical scaffold: the correctly-signed
// handler stub, plus the proto snippet when the RPC is not in the proto
// yet (printSnippet).
// pbImportPathFor resolves the generated proto package a service's
// handlers import: forge.yaml's module_path + the descriptor layout
// `gen/services/<svc>/v1`. This is the same path `forge scaffold service`
// writes into service.go, so a stub written here matches its neighbours.
func pbImportPathFor(root, svc string) (string, error) {
	configPath := filepath.Join(root, "forge.yaml")
	cfg, err := generator.ReadProjectConfig(configPath)
	if err != nil {
		return "", cliutil.WrapUserErr("forge scaffold rpc",
			"read project config", configPath,
			"verify forge.yaml is valid YAML", err)
	}
	if cfg.ModulePath == "" {
		return "", cliutil.UserErr("forge scaffold rpc",
			"forge.yaml declares no module_path", configPath,
			"set module_path so the generated proto import can be resolved")
	}
	return cfg.ModulePath + "/gen/services/" + naming.ServicePackage(svc) + "/v1", nil
}

func writeBareRPCStub(ctxLabel, handlerDir, pkg, rpcName, pbImportPath string, mode rpcStreamMode, printSnippet bool) error {
	// One file per RPC keeps the diff readable and avoids any merge with
	// existing handler files. mock_gen.go discovers methods across every
	// non-test .go file in the package, so the new file is auto-picked
	// up at the next `forge generate`.
	//
	// The name comes from codegen, not from a second copy of the rule here:
	// the generate pipeline's stub scaffolder writes the SAME file for the
	// same RPC (writeRPCHandlerStubs), so whichever order the user works in
	// — scaffold-then-declare or declare-then-generate — the RPC ends up in
	// one place with one name.
	fileName := codegen.RPCHandlerFileName(rpcName)
	targetPath := filepath.Join(handlerDir, fileName)
	if _, err := os.Stat(targetPath); err == nil {
		return cliutil.UserErr(ctxLabel,
			fmt.Sprintf("file %s already exists", targetPath),
			"",
			"pick a different <Name> (or delete the existing file first if you really want to start over)")
	}

	content := buildRPCHandlerStub(pkg, rpcName, pbImportPath, mode)
	if err := atomicfile.WriteFile(targetPath, []byte(content), 0o644); err != nil {
		return cliutil.WrapUserErr(ctxLabel, "write rpc file", targetPath,
			"verify the directory is writable", err)
	}

	fmt.Printf("✅ Created %s\n", targetPath)
	fmt.Println()
	fmt.Println("Next steps:")
	if printSnippet {
		fmt.Println("  1. Paste the snippet below into your service's .proto file")
		fmt.Println("     (inside the `service { ... }` block; the request/response messages")
		fmt.Println("     go at the bottom alongside the other message definitions).")
		fmt.Println("  2. Run `forge generate` to refresh codegen.")
		fmt.Println("  3. Fill in the handler body — the stub returns Unimplemented.")
		fmt.Println()
		fmt.Println("─── Proto snippet ──────────────────────────────────────────────────")
		fmt.Print(buildRPCProtoSnippet(rpcName, mode))
		fmt.Println("────────────────────────────────────────────────────────────────────")
	} else {
		fmt.Println("  1. Run `forge generate` to refresh codegen.")
		fmt.Println("  2. Fill in the handler body — the stub returns Unimplemented.")
	}
	return nil
}

// startsUpper reports whether s starts with an ASCII uppercase letter.
// Connect-RPC method names are exported Go identifiers, so we require
// the standard A-Z start here even though validateIdentifier is laxer.
func startsUpper(s string) bool {
	if s == "" {
		return false
	}
	return s[0] >= 'A' && s[0] <= 'Z'
}

// buildRPCHandlerStub returns the handler-file content for the chosen
// stream mode. The four signature shapes mirror codegen's
// buildMethodSignature (internal/codegen/generator.go) so a regenerated
// project doesn't diff against the scaffold output.
//
// The stub routes through svcerr so the Unimplemented result carries a
// stable "unimplemented" error-reason (surfaced as the x-forge-error-reason
// metadata the frontend's typed-error contract reads) while keeping the
// Connect CodeUnimplemented wire code. `nil, <errExpr>` for
// unary/client-streaming (where the return signature includes a *Response)
// and a bare `<errExpr>` for server/bidi (where the return is just
// `error`). Either way `forge generate && go build` succeeds out of
// the box; the user fills in the body when ready.
//
// The method carries the `forge:gen unwired-stub` marker, exactly as the
// generate pipeline's handler templates do. The marker — not the file's
// name and not its body — is what tells audit, CRUD gen's stub→shim
// excision, and any orchestrator reading the tree that this handler is
// forge-emitted pending work. A stub emitted without it is invisible to
// all three, which is what this command shipped until it was caught: it
// produced the same placeholder as `forge generate` and none of the
// signal.
func buildRPCHandlerStub(pkg, rpcName, pbImportPath string, mode rpcStreamMode) string {
	// rpcName is the PROTO spelling (the snippet declares it verbatim). The
	// stub spells Go identifiers, which the generators derive from it by
	// their own rule: rpc GetOauth2token is the handler method
	// GetOauth2Token taking a pb.GetOauth2TokenRequest. Declaring the
	// method under the proto spelling compiles — the embedded
	// Unimplemented handler supplies the real one — and never serves.
	goMethod := naming.GoCamelCase(rpcName)
	reqType := naming.GoCamelCase(rpcName + "Request")
	respType := naming.GoCamelCase(rpcName + "Response")
	errExpr := fmt.Sprintf(`svcerr.Wrap(svcerr.ScaffoldStub(%q))`, goMethod)

	// Qualify the request/response types with the generated proto
	// package. They live in gen/, never in the handler package.
	reqType = "pb." + reqType
	respType = "pb." + respType
	rpcName = goMethod // everything below — signature, doc line, marker — spells the Go method

	var sig, body string
	switch mode {
	case rpcServerStream:
		sig = fmt.Sprintf(
			"func (s *Service) %s(ctx context.Context, req *connect.Request[%s], stream *connect.ServerStream[%s]) error",
			rpcName, reqType, respType,
		)
		body = "\treturn " + errExpr
	case rpcClientStream:
		sig = fmt.Sprintf(
			"func (s *Service) %s(ctx context.Context, stream *connect.ClientStream[%s]) (*connect.Response[%s], error)",
			rpcName, reqType, respType,
		)
		body = "\treturn nil, " + errExpr
	case rpcBidiStream:
		sig = fmt.Sprintf(
			"func (s *Service) %s(ctx context.Context, stream *connect.BidiStream[%s, %s]) error",
			rpcName, reqType, respType,
		)
		body = "\treturn " + errExpr
	default: // rpcUnary
		sig = fmt.Sprintf(
			"func (s *Service) %s(ctx context.Context, req *connect.Request[%s]) (*connect.Response[%s], error)",
			rpcName, reqType, respType,
		)
		body = "\treturn nil, " + errExpr
	}

	// The generated-proto import is resolved, not templated. forge.yaml's
	// module_path plus the service name give the exact package the rest of
	// this handler directory already imports, so the file compiles as
	// written. It used to ship a commented placeholder and unqualified
	// type names, on the theory that "the user will fix the placeholder
	// when the file fails to compile" — which made the one command the
	// charter names for this job emit code that broke `forge generate`.
	//
	// The explanatory block is the METHOD's doc comment, not a floating
	// comment under `package`. Anywhere else and revive's `exported` rule
	// fires on the exported method — so forge's own `forge lint` rejected
	// forge's own freshly-scaffolded file, on a project with nothing else
	// in it. The marker is the LAST doc line for the same reason: the
	// FIRST one has to start with the method name.
	nolint := ""
	if naming.TripsReviveVarNaming(rpcName) {
		nolint = "//nolint:revive // var-naming: name fixed by the generated Handler interface (proto rpc " + rpcName + ")\n"
	}
	return fmt.Sprintf(`package %s

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "%s"
)

// %s is a hand-written RPC stub. Returned by `+"`forge scaffold rpc`"+`.
// Fill in the body when ready; the stub returns Unimplemented so
// `+"`go build`"+` succeeds before you have an implementation.
//
// Declare the RPC in the service .proto (the snippet is printed for you),
// then run `+"`forge generate`"+` so %s / %s exist.
%s
%s%s {
%s
}
`,
		pkg,
		pbImportPath,
		rpcName,
		reqType,
		respType,
		codegen.UnwiredStubMarkerComment(pkg, rpcName),
		nolint,
		sig,
		body,
	)
}

// buildRPCProtoSnippet returns the proto block the user pastes into
// the service's .proto file. The snippet has two parts: the rpc line
// (goes inside the service block) and the request/response messages
// (go at the bottom).
//
// We print the rpc line with the streaming keyword in the right slot
// per https://protobuf.dev/reference/protobuf/proto3-spec/#service_definition —
// the same shape Connect/buf expect.
func buildRPCProtoSnippet(rpcName string, mode rpcStreamMode) string {
	reqType := rpcName + "Request"
	respType := rpcName + "Response"

	var rpcLine string
	switch mode {
	case rpcServerStream:
		rpcLine = fmt.Sprintf("  rpc %s(%s) returns (stream %s);", rpcName, reqType, respType)
	case rpcClientStream:
		rpcLine = fmt.Sprintf("  rpc %s(stream %s) returns (%s);", rpcName, reqType, respType)
	case rpcBidiStream:
		rpcLine = fmt.Sprintf("  rpc %s(stream %s) returns (stream %s);", rpcName, reqType, respType)
	default:
		rpcLine = fmt.Sprintf("  rpc %s(%s) returns (%s);", rpcName, reqType, respType)
	}

	return fmt.Sprintf(`
// Paste inside `+"`service <YourService> { ... }`"+`:
%s

// Paste alongside the other message definitions:
message %s {
  // TODO: request fields
}

message %s {
  // TODO: response fields
}

`, rpcLine, reqType, respType)
}
