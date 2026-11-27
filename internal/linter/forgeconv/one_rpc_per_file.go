// File: internal/linter/forgeconv/one_rpc_per_file.go
//
// forgeconv-one-rpc-per-file — a handler file that implements more than one
// custom rpc.
//
// # The convention
//
// A service's custom rpcs live one per file: internal/handlers/<svc>/
// rpc_<snake_name>.go, a method on the package's *Service. `forge scaffold
// rpc` and `forge generate` both birth that file (codegen.RPCHandlerFileName),
// so a project forge scaffolded starts there. Per-rpc files keep a diff
// scoped to the rpc it changes, let two authors implement two rpcs of one
// service without merging, and make "where is SendMessage" a filename.
//
// Nothing said so once a project drifted: reliant carried 11 handler files
// holding 2 to 11 rpcs each (connection/connection.go held 11) and
// `forge lint --conventions` was clean.
//
// # What it checks
//
// For every managed service declared under proto/, the handler package forge
// resolves for it (codegen.ResolveServiceComponent — the directory generate,
// scaffold and `forge project shapes` use). In each of that package's own
// files — not a test, not a generated file — the methods whose name is one of
// the service's custom rpcs are counted per receiver type. More than one on
// the same receiver is a finding, one per file, naming every rpc and the file
// each moves to.
//
// Not counted:
//
//   - CRUD rpcs. An rpc is CRUD when codegen.CRUDShape claims it (unary,
//     Create/Get/Update/Delete<E> or List<Es>, no `// forge:custom`) and <E>
//     is a declared message in the service's proto package. Their shims
//     share handlers_crud.go by design, and that file is skipped whole.
//     A CRUD-named rpc marked `// forge:custom` IS custom; forge births it in its own rpc file
//     like any other, so it counts.
//   - Helpers. A method whose name is no rpc of the service (an unexported
//     helper, Name, Register) is not an rpc, whatever its receiver.
//   - Services marked `// forge:unmanaged`: forge owns none of their handler
//     layout.
//
// # Severity
//
// Advisory, permanently: a warning at every forge pin. Where an rpc's code
// sits changes no behaviour, so a layout rule must never fail a build on its
// own say-so. A project that has converged raises it with `lint.rules:
// {forgeconv-one-rpc-per-file: error}` (its rollout row is what lets it), and
// a file kept on purpose takes
// `// forge:lint-disable-file forgeconv-one-rpc-per-file: <why>`.
package forgeconv

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/jinzhu/inflection"

	"github.com/reliant-labs/forge/internal/codegen"
)

const oneRPCPerFileRule = "forgeconv-one-rpc-per-file"

// crudShimsFile is the owned file the CRUD shims share by design.
const crudShimsFile = "handlers_crud.go"

// LintOneRPCPerFile reports each handler file under rootDir that implements
// more than one custom rpc of its service. A project with no
// internal/handlers or no proto/ has nothing to report.
func LintOneRPCPerFile(rootDir string) (Result, error) {
	if _, err := os.Stat(filepath.Join(rootDir, "internal", "handlers")); err != nil {
		return Result{}, nil //nolint:nilerr // no handler tree: nothing to lint
	}
	scans, err := scanProjectProtos(rootDir)
	if err != nil {
		return Result{}, err
	}

	// A CRUD-shaped name must name a message in the RPC's own proto
	// package. The table half of the verdict needs the applied schema.
	messages := map[string]map[string]bool{}
	for _, s := range scans {
		for _, m := range s.Messages {
			if messages[s.Package] == nil {
				messages[s.Package] = map[string]bool{}
			}
			messages[s.Package][strings.ToLower(m.Name)] = true
		}
	}

	// service → its custom rpcs.
	custom := map[string]map[string]bool{}
	for _, s := range scans {
		for _, r := range s.RPCs {
			if r.Service == "" || s.ServiceUnmanaged(r.Service) || isCRUDRPC(r, messages[s.Package]) {
				continue
			}
			if custom[r.Service] == nil {
				custom[r.Service] = map[string]bool{}
			}
			custom[r.Service][r.Name] = true
		}
	}

	// handler dir → the custom rpcs of every service resolving to it.
	dirRPCs := map[string]map[string]bool{}
	for svc, rpcs := range custom {
		res, rerr := codegen.ResolveServiceComponent(rootDir, svc)
		if rerr != nil || !res.FromDisk {
			continue // no handler package yet, or one another lane reports
		}
		if dirRPCs[res.Dir] == nil {
			dirRPCs[res.Dir] = map[string]bool{}
		}
		for r := range rpcs {
			dirRPCs[res.Dir][r] = true
		}
	}

	dirs := make([]string, 0, len(dirRPCs))
	for d := range dirRPCs {
		dirs = append(dirs, d)
	}
	sort.Strings(dirs)

	var result Result
	for _, dir := range dirs {
		fs, lerr := lintOneRPCPerFileDir(rootDir, dir, dirRPCs[dir])
		if lerr != nil {
			return Result{}, lerr
		}
		result.Findings = append(result.Findings, fs...)
	}
	sort.SliceStable(result.Findings, func(i, j int) bool {
		if result.Findings[i].File != result.Findings[j].File {
			return result.Findings[i].File < result.Findings[j].File
		}
		return result.Findings[i].Line < result.Findings[j].Line
	})
	return result, nil
}

// isCRUDRPC reports whether forge treats r as a CRUD rpc: CRUDShape claims
// it and its entity is a declared message (List's plural singularized, as
// every CRUD matcher does).
func isCRUDRPC(r codegen.RawProtoRPC, messages map[string]bool) bool {
	op, entity := codegen.CRUDShape(codegen.Method{Name: r.Name, Custom: r.Custom, ServerStreaming: r.Streaming})
	if op == "" {
		return false
	}
	if op == "list" {
		entity = inflection.Singular(entity)
	}
	return messages[strings.ToLower(entity)]
}

// scanProjectProtos raw-scans each .proto under rootDir/proto separately so
// one file's package declaration cannot qualify a different file's RPCs.
// The vendored forge annotations under proto/forge/ are not the project's.
func scanProjectProtos(rootDir string) ([]*codegen.RawProtoScan, error) {
	protoRoot := filepath.Join(rootDir, "proto")
	var scans []*codegen.RawProtoScan
	err := filepath.WalkDir(protoRoot, func(p string, d fs.DirEntry, werr error) error {
		if werr != nil {
			return werr
		}
		if d.IsDir() {
			vendored := d.Name() == "forge" && filepath.Dir(p) == protoRoot
			if vendored || d.Name() == "node_modules" || d.Name() == ".buf" {
				return filepath.SkipDir
			}
			return nil
		}
		if strings.HasSuffix(p, ".proto") {
			s, err := codegen.ScanRawProtoFiles([]string{p})
			if err != nil {
				return fmt.Errorf("scan %s: %w", p, err)
			}
			scans = append(scans, s)
		}
		return nil
	})
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, fmt.Errorf("walk %s: %w", protoRoot, err)
	}
	return scans, nil
}

// rpcMethod is one rpc implementation found in a handler file.
type rpcMethod struct {
	name string
	line int
}

// lintOneRPCPerFileDir checks the handler package in dir against rpcs, the
// custom rpcs of the service(s) it implements.
func lintOneRPCPerFileDir(rootDir, dir string, rpcs map[string]bool) ([]Finding, error) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("read %s: %w", dir, err)
	}
	var findings []Finding
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") ||
			strings.HasSuffix(name, "_gen.go") || name == crudShimsFile {
			continue
		}
		path := filepath.Join(dir, name)
		fset := token.NewFileSet()
		file, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution|parser.ParseComments)
		if perr != nil || ast.IsGenerated(file) {
			continue // a file that does not parse fails the build; it is not this rule's to report
		}
		byRecv := map[string][]rpcMethod{}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv == nil || len(fn.Recv.List) == 0 || !rpcs[fn.Name.Name] {
				continue
			}
			recv := receiverTypeName(fn.Recv.List[0].Type)
			byRecv[recv] = append(byRecv[recv], rpcMethod{fn.Name.Name, fset.Position(fn.Pos()).Line})
		}
		rel, relErr := filepath.Rel(rootDir, path)
		if relErr != nil {
			rel = path
		}
		recvs := make([]string, 0, len(byRecv))
		for r := range byRecv {
			recvs = append(recvs, r)
		}
		sort.Strings(recvs)
		for _, recv := range recvs {
			if ms := byRecv[recv]; len(ms) > 1 {
				findings = append(findings, oneRPCPerFileFinding(filepath.ToSlash(rel), ms))
			}
		}
	}
	return findings, nil
}

// oneRPCPerFileFinding reports the rpcs ms (in source order, two or more)
// sharing the file rel. It sits on the second rpc, the first that has to
// move. An rpc whose own file this already is stays; every other is named
// with the file it moves to.
func oneRPCPerFileFinding(rel string, ms []rpcMethod) Finding {
	base := filepath.Base(rel)
	names := make([]string, len(ms))
	var moves []string
	for i, m := range ms {
		names[i] = m.name
		if target := codegen.RPCHandlerFileName(m.name); target != base {
			moves = append(moves, m.name+" → "+target)
		}
	}
	return Finding{
		Rule:     oneRPCPerFileRule,
		Severity: SeverityWarning,
		File:     rel,
		Line:     ms[1].line,
		Message: fmt.Sprintf("file implements %d rpcs (%s); forge convention is one custom rpc per file, rpc_<name>.go",
			len(ms), strings.Join(names, ", ")),
		Remediation: fmt.Sprintf("move each rpc method into its own file in the same package, the name `forge scaffold rpc` "+
			"gives it: %s. Helpers several rpcs share can stay put. A file kept whole on purpose takes "+
			"`// forge:lint-disable-file %s: <why>`", strings.Join(moves, ", "), oneRPCPerFileRule),
	}
}

// receiverTypeName is the type a method's receiver names, without the
// pointer or type parameters: *Service, Service and *Service[T] are all
// "Service".
func receiverTypeName(expr ast.Expr) string {
	for {
		switch x := expr.(type) {
		case *ast.StarExpr:
			expr = x.X
		case *ast.IndexExpr:
			expr = x.X
		case *ast.IndexListExpr:
			expr = x.X
		case *ast.ParenExpr:
			expr = x.X
		case *ast.Ident:
			return x.Name
		default:
			return ""
		}
	}
}
