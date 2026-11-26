// File: internal/codegen/composition_graph_lint.go
//
// `forge lint --graph` (docs/design/composition-graph.md §3 "Completeness",
// §5 "Audience"): the static checks generate cannot make because they are
// about the WHOLE graph rather than one node — and that the boot-time
// RequireComplete gate made before the graph.
//
//   - REACH. Every handler, package, worker and operator component, and
//     every provider, is reached by some root. A node no root reaches is
//     never constructed, so a service in that position is a service no
//     process serves — the exact gap RequireComplete failed boot over. A
//     provider kept for components to come says so with //forge:standby.
//     An internal package NOTHING consumes yet (every freshly scaffolded
//     one) is a pending warning, not an error: see pendingPackageFinding.
//     A node no root reaches whose Deps forge cannot fill is left out of
//     the emitted graph rather than failing generate; it is always an
//     error here (unbuiltFinding, composition_graph_reach.go). A component
//     constructed only by its variants (`forge scaffold variant`) is reached
//     when one of them is: see variantReached.
//   - AUDIENCE. For every listener (a node whose value is a
//     serverkit.Listener), the audience of each service its routes reach
//     must be the audience of the authenticator its chain reaches (a
//     provider marked //forge:audience <name>). The listener refuses the
//     same mismatch at boot (serverkit.checkAudience); this proves it before
//     anything runs, through a hand-written route set too.
//
// Both judge the graph exactly as generate assembles it (assembleGraph),
// writing nothing.

package codegen

import (
	"fmt"
	"go/ast"
	"go/types"
	"path/filepath"
	"sort"
	"strings"
)

// Graph lint rule ids.
const (
	GraphRuleUnreachable = "forge-graph-unreachable"
	GraphRuleAudience    = "forge-graph-audience"
	GraphRuleAssemble    = "forge-graph-assemble"
)

// GraphFinding is one composition-graph lint finding. File is
// project-relative.
type GraphFinding struct {
	Rule    string
	Error   bool
	File    string
	Line    int
	Message string
	Fix     string
}

// LintGraph judges a project's composition graph. graphMode is false (and
// nothing is judged) for a project on the legacy Infra / NewComponents
// composition. A graph that does not assemble — the error generate would
// fail with — is one error finding.
func LintGraph(projectDir string) (findings []GraphFinding, graphMode bool, err error) {
	if !GraphComposition(projectDir) {
		return nil, false, nil
	}
	in, err := graphLintInput(projectDir)
	if err != nil {
		return nil, true, err
	}
	m, aerr := assembleGraph(in)
	if aerr != nil {
		return []GraphFinding{{
			Rule: GraphRuleAssemble, Error: true, File: "internal/app/compose.go",
			Message: "the composition graph does not assemble: " + aerr.Error(),
			Fix:     "run `forge generate`, which fails with the same error and names the node, field and type",
		}}, true, nil
	}
	findings = append(findings, m.unreachableFindings()...)
	findings = append(findings, m.audienceFindings()...)
	findings = append(findings, m.optionalComponentFindings()...)
	sort.SliceStable(findings, func(i, j int) bool {
		if findings[i].File != findings[j].File {
			return findings[i].File < findings[j].File
		}
		return findings[i].Line < findings[j].Line
	})
	return findings, true, nil
}

// graphLintInput discovers the component set the way generate's composition
// step does.
func graphLintInput(projectDir string) (GraphGenInput, error) {
	module, err := GetModulePath(projectDir)
	if err != nil {
		return GraphGenInput{}, err
	}
	declared, err := ParseDeclaredServices(projectDir)
	if err != nil {
		return GraphGenInput{}, fmt.Errorf("parse service protos: %w", err)
	}
	names, err := DiscoverInternalPackageNames(projectDir)
	if err != nil {
		return GraphGenInput{}, fmt.Errorf("discover internal packages: %w", err)
	}
	packages, err := PackageDataFromNames(names, projectDir)
	if err != nil {
		return GraphGenInput{}, err
	}
	workers, err := WorkerDataFromSpecs(DiscoverWorkerSpecs(projectDir), projectDir)
	if err != nil {
		return GraphGenInput{}, fmt.Errorf("discover workers: %w", err)
	}
	operators, err := OperatorDataFromSpecs(DiscoverOperatorSpecs(projectDir), projectDir)
	if err != nil {
		return GraphGenInput{}, fmt.Errorf("discover operators: %w", err)
	}
	return GraphGenInput{
		GenContext: GenContext{ProjectDir: projectDir, ModulePath: module},
		Services:   ManagedServices(declared),
		Packages:   packages,
		Workers:    workers,
		Operators:  operators,
	}, nil
}

// reached is every node some root's closure holds.
func (m *graphModel) reached() map[string]bool {
	out := map[string]bool{}
	for _, v := range m.rootViews {
		for _, n := range v.Reach {
			out[n] = true
		}
	}
	return out
}

// rootNames lists the roots, for messages.
func (m *graphModel) rootNames() string {
	names := make([]string, len(m.roots))
	for i, r := range m.roots {
		names[i] = r.TypeName
	}
	return strings.Join(names, ", ")
}

// unreachableFindings reports every component and provider no root reaches.
// Route sets and the operators' manager are reported through their members.
func (m *graphModel) unreachableFindings() []GraphFinding {
	reached := m.reached()
	for n := range m.variantReached(reached) {
		reached[n] = true
	}
	consumed := m.consumed()
	audienceOf := m.serviceAudiences()
	var out []GraphFinding
	for _, n := range m.nodes {
		if reached[n.Name] {
			continue
		}
		if why, left := m.unbuilt[n.Name]; left {
			if f, ok := m.unbuiltFinding(n, why, audienceOf[n.Name]); ok {
				out = append(out, f)
			}
			continue
		}
		switch n.Kind {
		case nodeComponent:
			if n.Role == "internal" && !consumed[n.Name] {
				out = append(out, m.pendingPackageFinding(n))
				continue
			}
			out = append(out, GraphFinding{
				Rule: GraphRuleUnreachable, Error: true,
				File:    filepath.ToSlash(n.Comp.ImportPath),
				Message: fmt.Sprintf("%s is reachable from no root (%s): no process constructs it", n.Doc, m.rootNames()),
				Fix:     m.unreachableComponentFix(n, audienceOf[n.Name]),
			})
		case nodeProvider:
			if n.Standby {
				continue
			}
			f := GraphFinding{
				Rule: GraphRuleUnreachable, Error: true, File: n.File, Line: n.Line,
				Message: fmt.Sprintf("provider New%s (%s) is reachable from no root (%s): nothing takes its value", n.Name, n.Type, m.rootNames()),
				Fix:     fmt.Sprintf("take %s in the Deps of a node a root reaches, delete New%s, or mark it `//forge:standby` to keep it ready for a component to come", n.Type, n.Name),
			}
			if n.Lifecycle.Run != "" {
				f.Message = fmt.Sprintf("provider New%s (%s) runs, but no root (%s) holds it: no process starts it", n.Name, n.Type, m.rootNames())
				f.Fix = fmt.Sprintf("add a field of type %s to the //forge:root struct of the process that should run it, delete New%s, or mark it `//forge:standby`", n.Type, n.Name)
			}
			out = append(out, f)
		}
	}
	return out
}

// variantReached is every component a node in reached constructs as a
// VARIANT: a provider whose value wraps the component's Service by embedding
// it, the shape `forge scaffold variant <pkg> <Name>` writes
// (`type <Name> struct{ <pkg>.Service }`, built by New<Name> calling
// <pkg>.New with that configuration). The graph identifies a node by its Go
// type, so a component with no default configuration — only variants — is
// taken by no consumer and drew no edge, and the variants' providers call its
// constructor themselves. Require a resolved constructor result flowing into
// the returned embed; a discarded call or a nil or supplied Service in the
// same wrapper shape is not evidence. The reach check must count it: otherwise the
// idiom the graph-checks skill documents could not retire the
// //forge:provided marker it replaces.
//
// Only the lint's view of reach changes. Generate's resolution is
// untouched: the component's own node is still left out of graph_gen.go
// when forge cannot fill its Deps, which is right — no root asks for the
// default configuration, and the variants supply their own.
func (m *graphModel) variantReached(reached map[string]bool) map[string]bool {
	if m.u == nil {
		return nil
	}
	wrapped := map[string]bool{} // "<pkg path>.<Name>" a reached provider embeds
	for _, n := range m.nodes {
		if n.Kind != nodeProvider || !reached[n.Name] {
			continue
		}
		calls := m.providerConstructorCalls(n.Name)
		for _, k := range embeddedTypeKeys(m.u.produced[n.Name]) {
			for _, component := range m.nodes {
				if component.Kind != nodeComponent || component.Comp == nil || namedTypeKey(m.u.produced[component.Name]) != k {
					continue
				}
				// Use the loaded function identity, not the import spelling:
				// aliases work and a local lookalike New cannot prove reach.
				pkg := k[:strings.LastIndex(k, ".")]
				// NewInstrumented is the generated New wrapped in the
				// package's middleware (middleware_gen.go): the same
				// construction, so it is the same evidence.
				ctor := component.Comp.compConstructor
				if calls[k+"="+pkg+"."+ctor] || (ctor == "New" && calls[k+"="+pkg+".NewInstrumented"]) {
					wrapped[k] = true
				}
			}
		}
	}
	if len(wrapped) == 0 {
		return nil
	}
	out := map[string]bool{}
	for _, n := range m.nodes {
		if n.Kind != nodeComponent || reached[n.Name] {
			continue
		}
		if k := namedTypeKey(m.u.produced[n.Name]); k != "" && wrapped[k] {
			out[n.Name] = true
		}
	}
	return out
}

// providerConstructorCalls records only constructors whose result flows into
// an embedded field of the returned wrapper. This deliberately recognizes a
// small, straight-line subset (including scaffolded error guards), not arbitrary
// provider control flow. Unknown flow leaves the component pending.
func (m *graphModel) providerConstructorCalls(name string) map[string]bool {
	out := map[string]bool{}
	if m.u.app == nil || m.u.app.TypesInfo == nil {
		return out
	}
	for _, file := range m.u.app.Syntax {
		for _, decl := range file.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if !ok || fd.Recv != nil || fd.Name.Name != "New"+name || fd.Body == nil {
				continue
			}
			return returnedVariantConstructors(fd.Body, name, m.u.produced[name], m.u.app.TypesInfo)
		}
	}
	return out
}

// returnedVariantConstructors follows constructor results through local
// assignments into a returned literal. Reject unsupported control flow rather
// than treating a discarded call (or an overwritten local) as reach evidence.
func returnedVariantConstructors(body *ast.BlockStmt, name string, produced types.Type, info *types.Info) map[string]bool {
	locals := map[string]string{}
	constructor := func(expr ast.Expr) string {
		if id, ok := expr.(*ast.Ident); ok {
			return locals[id.Name]
		}
		call, ok := expr.(*ast.CallExpr)
		if !ok {
			return ""
		}
		var id *ast.Ident
		switch fun := call.Fun.(type) {
		case *ast.SelectorExpr:
			id = fun.Sel
		case *ast.Ident:
			id = fun
		}
		fn, ok := info.Uses[id].(*types.Func)
		if !ok || fn.Pkg() == nil {
			return ""
		}
		return fn.Pkg().Path() + "." + fn.Name()
	}
	for _, stmt := range body.List {
		switch s := stmt.(type) {
		case *ast.AssignStmt:
			values := make([]string, len(s.Lhs))
			for i, rhs := range s.Rhs {
				values[i] = constructor(rhs)
			}
			for i, lhs := range s.Lhs {
				id, ok := lhs.(*ast.Ident)
				if !ok { // A field mutation can change the returned value.
					return nil
				}
				locals[id.Name] = values[i]
			}
		case *ast.IfStmt:
			// The scaffold's error guard may exit without a wrapper. Any
			// other branch needs control-flow analysis we do not claim here.
			if !variantErrorGuard(s) {
				return nil
			}
		case *ast.ReturnStmt:
			if len(s.Results) == 0 {
				return nil
			}
			expr := s.Results[0]
			if addr, ok := expr.(*ast.UnaryExpr); ok {
				expr = addr.X
			}
			lit, ok := expr.(*ast.CompositeLit)
			if !ok {
				return nil
			}
			id, ok := lit.Type.(*ast.Ident)
			if !ok || id.Name != name {
				return nil
			}
			t := types.Unalias(produced)
			if p, ok := t.(*types.Pointer); ok {
				t = p.Elem()
			}
			st, ok := t.Underlying().(*types.Struct)
			if !ok {
				return nil
			}
			out := map[string]bool{}
			for i, elt := range lit.Elts {
				fieldIndex, value := i, elt
				if kv, ok := elt.(*ast.KeyValueExpr); ok {
					key, ok := kv.Key.(*ast.Ident)
					if !ok {
						continue
					}
					fieldIndex = st.NumFields()
					for j := 0; j < st.NumFields(); j++ {
						if st.Field(j).Name() == key.Name {
							fieldIndex = j
							break
						}
					}
					value = kv.Value
				}
				if fieldIndex < st.NumFields() && st.Field(fieldIndex).Embedded() {
					if fn := constructor(value); fn != "" {
						out[namedTypeKey(st.Field(fieldIndex).Type())+"="+fn] = true
					}
				}
			}
			return out
		default:
			return nil
		}
	}
	return nil
}

func variantErrorGuard(s *ast.IfStmt) bool {
	if s.Init != nil || s.Else != nil || len(s.Body.List) != 1 {
		return false
	}
	cond, ok := s.Cond.(*ast.BinaryExpr)
	if !ok {
		return false
	}
	_, leftIdent := cond.X.(*ast.Ident)
	right, rightIdent := cond.Y.(*ast.Ident)
	if !leftIdent || !rightIdent || right.Name != "nil" {
		return false
	}
	r, ok := s.Body.List[0].(*ast.ReturnStmt)
	if !ok || len(r.Results) == 0 {
		return false
	}
	id, ok := r.Results[0].(*ast.Ident)
	return ok && id.Name == "nil"
}

// embeddedTypeKeys is the namedTypeKey of every field t's struct embeds,
// through one pointer (a provider returns *<Name>).
func embeddedTypeKeys(t types.Type) []string {
	if t == nil {
		return nil
	}
	if p, ok := types.Unalias(t).(*types.Pointer); ok {
		t = p.Elem()
	}
	st, ok := types.Unalias(t).Underlying().(*types.Struct)
	if !ok {
		return nil
	}
	var out []string
	for i := 0; i < st.NumFields(); i++ {
		if f := st.Field(i); f.Embedded() {
			if k := namedTypeKey(f.Type()); k != "" {
				out = append(out, k)
			}
		}
	}
	return out
}

// namedTypeKey identifies a named type, through one pointer, by its
// package path and name — not by types.Identical, because the component's
// package and internal/app may be type-checked in separate loads.
func namedTypeKey(t types.Type) string {
	if t == nil {
		return ""
	}
	if p, ok := types.Unalias(t).(*types.Pointer); ok {
		t = p.Elem()
	}
	named, ok := types.Unalias(t).(*types.Named)
	if !ok || named.Obj() == nil || named.Obj().Pkg() == nil {
		return ""
	}
	return named.Obj().Pkg().Path() + "." + named.Obj().Name()
}

// unbuiltFinding reports a node no root reaches that forge cannot construct
// (composition_graph_reach.go). It is an ERROR whatever else holds — a
// package nothing consumes, a //forge:standby provider — because before reach
// scoped resolution it failed generate, and the default posture must keep
// every error a project had. Route sets and the operators' manager are
// reported through their members.
func (m *graphModel) unbuiltFinding(n *graphNode, why unbuiltNode, audience string) (GraphFinding, bool) {
	const tail = "; once a root reaches it, generate names every provider it still lacks"
	switch n.Kind {
	case nodeComponent:
		return GraphFinding{
			Rule: GraphRuleUnreachable, Error: true,
			File:    filepath.ToSlash(n.Comp.ImportPath),
			Message: fmt.Sprintf("%s is reachable from no root (%s), and forge cannot construct it — %s — so it is left out of graph_gen.go: no process builds it", n.Doc, m.rootNames(), why.describe()),
			Fix:     m.unreachableComponentFix(n, audience) + tail,
		}, true
	case nodeProvider:
		return GraphFinding{
			Rule: GraphRuleUnreachable, Error: true, File: n.File, Line: n.Line,
			Message: fmt.Sprintf("provider New%s (%s) is reachable from no root (%s), and forge cannot construct it — %s — so it is left out of graph_gen.go", n.Name, n.Type, m.rootNames(), why.describe()),
			Fix:     fmt.Sprintf("take %s in the Deps of a node a root reaches, or delete New%s", n.Type, n.Name) + tail,
		}, true
	}
	return GraphFinding{}, false
}

// consumed is every node some other node's compose.go method names — called
// or as a lazy method value — whether or not a root reaches the consumer.
func (m *graphModel) consumed() map[string]bool {
	out := map[string]bool{}
	for _, deps := range []map[string][]string{m.edges, m.lazy} {
		for _, targets := range deps {
			for _, t := range targets {
				out[t] = true
			}
		}
	}
	return out
}

// pendingPackageFinding reports an internal package that nothing consumes
// yet, as a WARNING.
//
// A package runs nothing and serves nothing: it exists only as another
// node's dependency, and in the graph it is constructed only when a
// consumer's Deps takes it. So `forge scaffold package <name>` always births
// one with no consumer, and an error here made forge's own fresh output fail
// forge's own gate (TestE2EComponentScaffoldsLintClean), the state
// lint_pending_stubs.go and scaffold-not-customized already hold at warning.
// A package with no consumer is pending work, not a gap a process falls
// into. Once something takes it, its reach is the consumer's, and a consumer
// no root reaches is an error, reported for both.
func (m *graphModel) pendingPackageFinding(n *graphNode) GraphFinding {
	return GraphFinding{
		Rule: GraphRuleUnreachable, Error: false,
		File: filepath.ToSlash(n.Comp.ImportPath),
		Message: fmt.Sprintf("pending: nothing takes %s in its Deps yet — %s is reachable from no root (%s), so no process constructs it; "+
			"a warning while it has no consumer, an error once a consumer no root reaches takes it", n.Type, n.Doc, m.rootNames()),
		Fix: fmt.Sprintf("take %s in the Deps of a node a root reaches, or delete the package", n.Type),
	}
}

func (m *graphModel) unreachableComponentFix(n *graphNode, audience string) string {
	switch {
	case n.Role == "internal/handlers" && audience != "":
		routes := audienceRoutesName(audience)
		return fmt.Sprintf("a service is served by a listener whose Routes is its audience's route set: give some root a listener over %s (g.%s() in its compose.go method) — or, for a sibling binary's service, mark the proto service `// forge:unmanaged`", routes, routes)
	case n.Role == "internal/handlers":
		return "mount it on a listener some root holds, or mark the proto service `// forge:unmanaged`"
	case n.Role == "internal/workers" || n.Role == "internal/operators":
		return fmt.Sprintf("add a field of type %s to a //forge:root struct in internal/app (or, with no root declared, it joins the default Server root once it runs), or delete the package", n.Type)
	}
	return fmt.Sprintf("take %s in the Deps of a node a root reaches, or delete the package", n.Type)
}

// serviceAudiences maps each service node to its declared audience.
func (m *graphModel) serviceAudiences() map[string]string {
	out := map[string]string{}
	for _, sv := range m.routesView.Services {
		if n := m.compNode[sv.FieldName]; n != nil {
			out[n.Name] = sv.Audience
		}
	}
	return out
}

// serverkitPkg is the import path listener and chain identity is read from.
const serverkitPkg = "github.com/reliant-labs/forge/pkg/serverkit"

// audienceFindings checks every listener's services against its chain's
// authenticator.
func (m *graphModel) audienceFindings() []GraphFinding {
	audienceOf := m.serviceAudiences()
	var out []GraphFinding
	for _, l := range m.nodes {
		t, ok := m.u.produced[l.Name]
		if !ok || !embedsNamed(t, serverkitPkg, "Listener", 0) {
			continue
		}
		var auths, chains []string
		services := map[string]bool{}
		for _, d := range m.edges[l.Name] {
			dn := m.byName[d]
			if dn == nil {
				continue
			}
			// A connect-go v2 project's chains are serverkit.ChainV2.
			if dt, ok := m.u.produced[d]; ok && (embedsNamed(dt, serverkitPkg, "Chain", 0) || embedsNamed(dt, serverkitPkg, "ChainV2", 0)) {
				chains = append(chains, d)
				m.walk(d, func(x *graphNode) bool {
					if x.AuthAudience != "" {
						auths = append(auths, x.Name)
						return false
					}
					return true
				})
				continue
			}
			m.walk(d, func(x *graphNode) bool {
				if _, isSvc := audienceOf[x.Name]; isSvc {
					services[x.Name] = true
				}
				xt, typed := m.u.produced[x.Name]
				return !typed || !embedsNamed(xt, serverkitPkg, "Listener", 0)
			})
		}
		out = append(out, m.listenerAudienceFindings(l, chains, auths, services, audienceOf)...)
	}
	return out
}

func (m *graphModel) listenerAudienceFindings(l *graphNode, chains, auths []string, services map[string]bool, audienceOf map[string]string) []GraphFinding {
	if len(services) == 0 {
		return nil
	}
	file, line := l.File, l.Line
	if file == "" {
		file = "internal/app/compose.go"
	}
	auds := map[string]string{} // audience → authenticator node
	for _, a := range auths {
		auds[m.byName[a].AuthAudience] = a
	}
	if len(chains) == 0 || len(auds) == 0 {
		return []GraphFinding{{
			Rule: GraphRuleAudience, File: file, Line: line,
			Message: fmt.Sprintf("listener %s serves services, but the audience of its chain cannot be read statically: its chain reaches no authenticator provider marked //forge:audience (the listener still checks at boot)", l.Name),
			Fix:     "mark the authenticator's provider func `//forge:audience <name>` (the scaffolded NewUserAuth is `//forge:audience user`)",
		}}
	}
	if len(auds) > 1 {
		return []GraphFinding{{
			Rule: GraphRuleAudience, Error: true, File: file, Line: line,
			Message: fmt.Sprintf("listener %s's chain reaches authenticators of %d audiences (%s): one listener is one trust domain", l.Name, len(auds), strings.Join(graphSortedKeys(auds), ", ")),
			Fix:     "give each audience its own chain and listener",
		}}
	}
	var chainAud, auth string
	for a, n := range auds {
		chainAud, auth = a, n
	}
	var out []GraphFinding
	for _, svc := range graphSortedKeys(services) {
		if a := audienceOf[svc]; a != chainAud {
			out = append(out, GraphFinding{
				Rule: GraphRuleAudience, Error: true, File: file, Line: line,
				Message: fmt.Sprintf("listener %s serves %s (audience %q) through a chain authenticated by %s (audience %q): a %s caller would reach a %s service", l.Name, m.byName[svc].Doc, a, auth, chainAud, chainAud, a),
				Fix:     fmt.Sprintf("serve %s from a listener whose chain authenticates the %q audience — its route set is %s — or change the service's `(forge.v1.service).audience`", svc, a, audienceRoutesName(a)),
			})
		}
	}
	return out
}

// walk visits from's closure (from included), depth first; visit returning
// false stops the walk below that node.
func (m *graphModel) walk(from string, visit func(*graphNode) bool) {
	seen := map[string]bool{}
	var rec func(string)
	rec = func(name string) {
		if seen[name] {
			return
		}
		seen[name] = true
		n := m.byName[name]
		if n == nil || !visit(n) {
			return
		}
		for _, d := range m.edges[name] {
			rec(d)
		}
		for _, d := range m.lazy[name] {
			rec(d)
		}
	}
	rec(from)
}

// embedsNamed reports whether t is, points to, or (transitively) embeds the
// named type pkg.name.
func embedsNamed(t types.Type, pkg, name string, depth int) bool {
	if depth > 4 || t == nil {
		return false
	}
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	named, ok := t.(*types.Named)
	if !ok {
		return false
	}
	if obj := named.Obj(); obj.Pkg() != nil && obj.Pkg().Path() == pkg && obj.Name() == name {
		return true
	}
	st, ok := named.Underlying().(*types.Struct)
	if !ok {
		return false
	}
	for i := 0; i < st.NumFields(); i++ {
		if f := st.Field(i); f.Embedded() && embedsNamed(f.Type(), pkg, name, depth+1) {
			return true
		}
	}
	return false
}

func graphSortedKeys[V any](m map[string]V) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
