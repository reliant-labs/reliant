package contractcheck

import "go/ast"

// localTypeIsData applies the documented inert-data exemption to types
// declared beside Deps, including aliases and collections. Unlike a method-only
// purity inference, every nested field must be proven inert. Unknowns and cycles
// fail closed; a name such as Config never grants an exemption.
func (l *depsLinter) localTypeIsData(expr ast.Expr, dir string, imports map[string]string) bool {
	return l.localDataExpr(expr, l.packageTypeDecls(dir), imports, map[string]bool{}, 0)
}

func (l *depsLinter) localDataExpr(expr ast.Expr, decls typeDecls, imports map[string]string, visiting map[string]bool, depth int) bool {
	if depth > inertFieldDepth*4 {
		return false
	}
	switch t := expr.(type) {
	case *ast.Ident:
		if isPrimitiveType(t) {
			return true
		}
		underlying, ok := decls.types[t.Name]
		key := decls.dir + "\x00" + t.Name
		if !ok || visiting[key] || decls.embeds[t.Name] {
			return false
		}
		for _, m := range decls.methods[t.Name] {
			if !methodSignatureIsAccessorShaped(m, decls) {
				return false
			}
		}
		if !l.methodsAvoidPackageState(decls, t.Name) {
			return false
		}
		visiting[key] = true
		defer delete(visiting, key)
		return l.localDataExpr(underlying, decls, decls.typeImports[t.Name], visiting, depth+1)
	case *ast.StructType:
		for _, f := range t.Fields.List {
			if len(f.Names) == 0 || !l.localDataExpr(f.Type, decls, imports, visiting, depth+1) {
				return false
			}
		}
		return true
	case *ast.ArrayType:
		return l.localDataExpr(t.Elt, decls, imports, visiting, depth+1)
	case *ast.MapType:
		return l.scalarMapKey(t.Key, decls, imports, map[string]bool{}, depth+1) &&
			l.localDataExpr(t.Value, decls, imports, visiting, depth+1)
	case *ast.SelectorExpr:
		if isInertStdlibValueType(t) {
			return true
		}
		qualifier, ok := t.X.(*ast.Ident)
		if !ok {
			return false
		}
		dir := l.dirForQualifier(qualifier.Name, imports)
		if dir == "" {
			return false
		}
		return l.localDataExpr(ast.NewIdent(t.Sel.Name), l.packageTypeDecls(dir), nil, visiting, depth+1)
	}
	// A pointer, callback, interface or channel is a collaborator, even when
	// wrapped in a named alias or collection. No live-handle fallback here.
	return false
}

// scalarMapKey checks only scalar identity, not methods: map indexing never
// invokes a key's methods. This exemption is never used for dependency values.
func (l *depsLinter) scalarMapKey(expr ast.Expr, decls typeDecls, imports map[string]string, visiting map[string]bool, depth int) bool {
	if depth > inertFieldDepth*4 {
		return false
	}
	switch t := expr.(type) {
	case *ast.Ident:
		if isPrimitiveType(t) {
			return true
		}
		underlying, ok := decls.types[t.Name]
		key := decls.dir + "\x00" + t.Name
		if !ok || visiting[key] {
			return false
		}
		visiting[key] = true
		defer delete(visiting, key)
		return l.scalarMapKey(underlying, decls, decls.typeImports[t.Name], visiting, depth+1)
	case *ast.SelectorExpr:
		qualifier, ok := t.X.(*ast.Ident)
		if !ok {
			return false
		}
		dir := l.dirForQualifier(qualifier.Name, imports)
		if dir != "" {
			return l.scalarMapKey(ast.NewIdent(t.Sel.Name), l.packageTypeDecls(dir), nil, visiting, depth+1)
		}
	}
	return false
}
