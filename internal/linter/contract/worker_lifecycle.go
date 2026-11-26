package contract

import (
	"go/ast"
	"go/token"
	"go/types"
	"strings"
)

// hasWorkerLifecycle recognizes both the direct serverkit Name/Start surface
// and the Start/Stop surface accepted by lifecyclekit.WrapWorker, which supplies
// Name itself. Only declared, correctly typed methods on the same receiver in
// the package's own source count; a helper filed under workers is not a worker.
// Keep this shared by require-contract and the outbound opt-out gate.
func hasWorkerLifecycle(pkgPath string, files []*ast.File, fset *token.FileSet, info *types.Info) bool {
	if !strings.Contains(pkgPath, "/internal/workers/") && !strings.HasPrefix(pkgPath, "internal/workers/") {
		return false
	}
	if info == nil {
		return false
	}
	methods := map[*types.Named]map[string]bool{}
	for _, file := range files {
		if isNonSourceFile(fset.Position(file.Pos()).Filename) || isForgeGeneratedFile(file) {
			continue
		}
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Recv == nil {
				continue
			}
			fn, ok := info.Defs[fd.Name].(*types.Func)
			if !ok {
				continue
			}
			sig, ok := fn.Type().(*types.Signature)
			if !ok || sig.Recv() == nil || !isWorkerMethod(fn.Name(), sig) {
				continue
			}
			recv := sig.Recv().Type()
			if ptr, ok := recv.(*types.Pointer); ok {
				recv = ptr.Elem()
			}
			named, ok := recv.(*types.Named)
			if !ok {
				continue
			}
			// Generic receiver instantiations share their declared base type.
			named = named.Origin()
			if methods[named] == nil {
				methods[named] = map[string]bool{}
			}
			methods[named][fn.Name()] = true
		}
	}
	for _, set := range methods {
		if set["Start"] && (set["Name"] || set["Stop"]) {
			return true
		}
	}
	return false
}

func isWorkerMethod(name string, sig *types.Signature) bool {
	if sig.Variadic() || sig.Results().Len() != 1 {
		return false
	}
	if name == "Name" {
		return sig.Params().Len() == 0 && types.Identical(sig.Results().At(0).Type(), types.Typ[types.String])
	}
	if name != "Start" && name != "Stop" {
		return false
	}
	if sig.Params().Len() != 1 || !types.Identical(sig.Results().At(0).Type(), types.Universe.Lookup("error").Type()) {
		return false
	}
	ctx, ok := types.Unalias(sig.Params().At(0).Type()).(*types.Named)
	return ok && ctx.Obj().Pkg() != nil && ctx.Obj().Pkg().Path() == "context" && ctx.Obj().Name() == "Context"
}
