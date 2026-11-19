// File: internal/codegen/composition_graph_resolve.go
//
// Resolution: the value expression forge writes for a Deps key compose.go
// does not have yet. A key the owned compose.go already sets is NEVER
// resolved — the author's expression is the answer, which is also how an
// ambiguous match is settled. For a missing key, in order:
//
//  1. a graph INPUT, by type: context.Context → g.Ctx, *config.Config →
//     g.Cfg, *slog.Logger → g.Log, serverkit.Config → g.ServerCfg,
//     *serverkit.Probes → g.Probes, and each config the primary binary binds
//     beside the root config, *config.<Message> → g.<Message>;
//  2. a config BLOCK the root config composes, by type → g.Cfg.Get<Block>();
//  3. a framework SEAM: the clock, the id generator, a generated store
//     (<orm pkg>.NewStore over whatever node produces the orm.Context);
//  4. a PRODUCER, by type: the one node whose value is assignable to the
//     field (an identical type wins over a merely assignable one; a
//     //forge:root-only node is offered only to a field of its very type).
//     Two or more is an AMBIGUITY — a generate error until compose.go's
//     owned expression picks one. Names never resolve;
//  5. a scalar: the config field of that name, else its zero value (with a
//     hint that configuration belongs in a block);
//  6. an optional (`forge:optional-dep`) field with no producer: nil;
//  7. anything else is MISSING — a generate error naming the provider func
//     to write.

package codegen

import (
	"fmt"
	"go/types"
	"sort"
	"strings"
)

// graphResolver resolves missing Deps keys against the graph.
type graphResolver struct {
	module       string
	nodes        []*graphNode
	byName       map[string]*graphNode
	u            *graphUniverse
	blocks       configBlocks
	configFields map[string]InfraField
	store        storeSeam // the generated store, in database.orm.package
	// configInputs are the primary binary's configs beside the root
	// config, by message name: each is the graph input g.<Message>.
	configInputs map[string]bool
}

// graphProblem is an unresolvable key: missing, or ambiguous.
type graphProblem struct {
	Node       string
	Field      string
	Type       string
	Candidates []string // non-empty: ambiguous
}

// resolved is one resolved key.
type resolved struct {
	Expr    string
	Comment string
	Imports map[string]string
}

// graphInputs are the graph's inputs by the type a Deps field declares, as
// go/types prints it (fully qualified) — and the conventional spelling
// fallback when the field's type did not check.
func (r *graphResolver) inputFor(want types.Type, written string) (string, bool) {
	q := ""
	if want != nil && want != types.Typ[types.Invalid] {
		q = types.TypeString(want, nil)
	}
	switch {
	case q == "context.Context" || (q == "" && written == "context.Context"):
		return "g.Ctx", true
	case q == "*log/slog.Logger" || (q == "" && written == "*slog.Logger"):
		return "g.Log", true
	case q == "*"+r.module+"/pkg/config.Config" || (q == "" && written == "*config.Config"):
		return "g.Cfg", true
	case q == "github.com/reliant-labs/forge/pkg/serverkit.Config" || (q == "" && written == "serverkit.Config"):
		return "g.ServerCfg", true
	case q == "*github.com/reliant-labs/forge/pkg/serverkit.Probes" || (q == "" && written == "*serverkit.Probes"):
		return "g.Probes", true
	}
	// pkg/config's Config may be an alias of a generated message type;
	// resolve that by the written spelling too.
	if written == "*config.Config" {
		return "g.Cfg", true
	}
	// A config the primary binary binds beside the root config is a graph
	// input of its own: *config.<Message> (an alias of the generated
	// message, either spelling) is g.<Message>.
	for name := range r.configInputs {
		if q == "*"+r.module+"/pkg/config."+name || r.isGeneratedConfig(q, name) || written == "*config."+name {
			return "g." + name, true
		}
	}
	return "", false
}

// imports returns the alias → path map a node's Deps field types are written
// against: the component's import block, or the provider file's.
func (n *graphNode) depsImports() map[string]string {
	if n.Comp != nil {
		return n.Comp.compImports
	}
	return n.SrcImports
}

// canonicalType rewrites a written type's package aliases to import paths,
// so two spellings in different files compare equal.
func canonicalType(expr string, imports map[string]string) string {
	out := expr
	refs, _ := typeExprImports(expr, imports)
	for alias, path := range refs {
		out = strings.ReplaceAll(out, alias+".", path+".")
	}
	return out
}

// resolve returns the value expression for n's Deps field df, or a problem.
func (r *graphResolver) resolve(n *graphNode, df DepsField) (resolved, *graphProblem) {
	var want types.Type
	if f, ok := r.u.deps[n.Name]; ok {
		want = f[df.Name]
	}
	imports := n.depsImports()

	// 0. A generated route set's per-service options are configuration the
	// author fills in compose.go.
	if n.Kind == nodeRoutes && df.Name == "Options" {
		return resolved{Expr: "MountOptions{}", Comment: "per-service mountkit options on top of the listener's chain — yours"}, nil
	}

	// 1. Graph inputs.
	if expr, ok := r.inputFor(want, df.Type); ok {
		return resolved{Expr: expr}, nil
	}

	// 2. Config block.
	if expr, ok := r.blocks.resolve(df.Type, imports); ok {
		return resolved{Expr: strings.Replace(expr, "infra.", "g.", 1)}, nil
	}

	// 3. Framework seams.
	if !df.Optional {
		switch df.Type {
		case frameworkClockType:
			return resolved{Expr: frameworkClockExpr, Comment: "framework clock", Imports: map[string]string{"time": "time"}}, nil
		case frameworkIDGenType:
			return resolved{Expr: frameworkIDGenExpr, Comment: "framework id generator", Imports: map[string]string{"ulid": "github.com/oklog/ulid/v2"}}, nil
		}
		if r.store.owns(df.Type) {
			orm := r.ormProducer(n)
			if orm != nil {
				expr := r.store.pkg + ".NewStore(g." + orm.Name + "())"
				if plural, isEntity := r.store.accessors[df.Type]; isEntity {
					expr += "." + plural + "()"
				}
				return resolved{Expr: expr, Comment: r.store.source(), Imports: map[string]string{r.store.pkg: r.module + "/" + r.store.dir}}, nil
			}
		}
	}

	// 4. Producer by type.
	cands := r.u.producersOf(want, n.Name, r.nodes)
	if len(cands) == 0 {
		cands = r.producersByWrittenType(n, df.Type, imports)
	}
	switch len(cands) {
	case 1:
		return resolved{Expr: "g." + cands[0].Name + "()"}, nil
	case 0:
	default:
		names := make([]string, len(cands))
		for i, c := range cands {
			names[i] = "g." + c.Name + "()"
		}
		sort.Strings(names)
		return resolved{}, &graphProblem{Node: n.Name, Field: df.Name, Type: df.Type, Candidates: names}
	}

	// 4b. A LAZY edge: a field typed `func() T` gets the accessor of the one
	// node producing T as a method value (g.X, not g.X()), resolved on first
	// use — how a dependency cycle is broken. It draws no construction edge.
	if lazy := r.lazyProducers(n, want, df.Type, imports); len(lazy) == 1 {
		return resolved{Expr: "g." + lazy[0].Name, Comment: "lazy: resolved on first use"}, nil
	} else if len(lazy) > 1 {
		names := make([]string, len(lazy))
		for i, c := range lazy {
			names[i] = "g." + c.Name
		}
		sort.Strings(names)
		return resolved{}, &graphProblem{Node: n.Name, Field: df.Name, Type: df.Type, Candidates: names}
	}

	// 5. Scalars are configuration.
	if zeroValueLiteral(df.Type) != "nil" {
		if field, ok := matchScalarConfigField(df, r.configFields); ok {
			return resolved{Expr: "g.Cfg." + field, Comment: "from config"}, nil
		}
		return resolved{Expr: zeroValueLiteral(df.Type), Comment: fmt.Sprintf("TODO: %s is configuration — declare a config block (see forge architecture skill)", df.Name)}, nil
	}

	// 6. Optional. The comments are placeholders forge recognises again
	// (optionalPlaceholder): a key still holding one is re-resolved on every
	// generate, so the provider that arrives later fills it.
	if df.Optional {
		// A struct value cannot be nil: an optional one with no provider is
		// its zero value (`auth.Provider{}`), which is what "absent" means
		// for a field of that type.
		if want != nil && want != types.Typ[types.Invalid] {
			if _, isStruct := want.Underlying().(*types.Struct); isStruct {
				if imps, unknown := typeExprImports(df.Type, imports); len(unknown) == 0 {
					return resolved{Expr: df.Type + "{}", Comment: optionalZeroComment, Imports: imps}, nil
				}
			}
		}
		return resolved{Expr: "nil", Comment: optionalNilComment}, nil
	}
	return resolved{}, &graphProblem{Node: n.Name, Field: df.Name, Type: df.Type}
}

// The comments forge writes beside an optional key it found no producer for.
const (
	optionalNilComment  = "optional — no provider"
	optionalZeroComment = "optional — no provider: the zero value"
)

// optionalPlaceholder reports whether a key's value and trailing comment are
// the placeholder forge wrote for an optional field with no producer: `nil`,
// or a struct type's `T{}`, followed by the matching comment. Such a key is
// forge's, not the author's — the author never chose nil, there was nothing
// else to choose — so it is resolved again on every generate instead of
// freezing the absence: an optional dep whose provider arrives later (a root
// that comes to reach the node, a provider added after the consumer) is
// filled. An author who means "absent" for good writes the nil without the
// comment.
func optionalPlaceholder(value, comment string) bool {
	switch strings.TrimSpace(comment) {
	case optionalNilComment:
		return value == "nil"
	case optionalZeroComment:
		return strings.HasSuffix(value, "{}")
	}
	return false
}

// producersByWrittenType is the string-keyed fallback when go/types could not
// type the field: the nodes whose type, written with import paths, equals the
// field's.
func (r *graphResolver) producersByWrittenType(n *graphNode, written string, imports map[string]string) []*graphNode {
	want := canonicalType(written, imports)
	var out []*graphNode
	for _, c := range r.nodes {
		if c.Name == n.Name {
			continue
		}
		if canonicalType(c.Type, c.Imports) == want {
			out = append(out, c)
		}
	}
	return out
}

// ormProducer is the node a generated store is built over: the one whose
// value is the ORM client.
func (r *graphResolver) ormProducer(self *graphNode) *graphNode {
	var found *graphNode
	for _, c := range r.nodes {
		if c.Name == self.Name {
			continue
		}
		q := ""
		if t, ok := r.u.produced[c.Name]; ok {
			q = types.TypeString(t, nil)
		}
		if q == "*github.com/reliant-labs/forge/pkg/orm.Client" || canonicalType(c.Type, c.Imports) == "*github.com/reliant-labs/forge/pkg/orm.Client" {
			if found != nil {
				return nil
			}
			found = c
		}
	}
	return found
}

// graphProblemsError renders every unresolvable key as one generate error.
func graphProblemsError(problems []graphProblem) error {
	sort.Slice(problems, func(i, j int) bool {
		if problems[i].Node != problems[j].Node {
			return problems[i].Node < problems[j].Node
		}
		return problems[i].Field < problems[j].Field
	})
	var b strings.Builder
	var missing, ambiguous []graphProblem
	for _, p := range problems {
		if len(p.Candidates) > 0 {
			ambiguous = append(ambiguous, p)
		} else {
			missing = append(missing, p)
		}
	}
	b.WriteString("the composition graph (internal/app/compose.go) cannot fill every Deps field.\n")
	if len(ambiguous) > 0 {
		b.WriteString("\nAMBIGUOUS — more than one node's value is assignable, and forge never picks one:\n\n")
		for _, p := range ambiguous {
			fmt.Fprintf(&b, "  - %s.Deps.%s (%s) could be %s\n", p.Node, p.Field, p.Type, strings.Join(p.Candidates, " or "))
			fmt.Fprintf(&b, "      write the key yourself in compose.go's new%s — `%s: %s,` — and forge keeps your choice.\n", p.Node, p.Field, p.Candidates[0])
		}
		b.WriteString("\n  (If new<Node> does not exist yet, write the whole method: forge then fills only the keys it is missing.)\n")
	}
	if len(missing) > 0 {
		b.WriteString("\nMISSING — no node produces the type, and it is not a graph input, config block or scalar. forge will NOT emit a nil for a required dependency: it would pass validateDeps and nil-deref at runtime.\n\n")
		for _, p := range missing {
			fmt.Fprintf(&b, "  - %s.Deps.%s (%s) has no provider. Add one to internal/app/providers.go:\n\n", p.Node, p.Field, p.Type)
			x := providerNameFor(p.Field)
			fmt.Fprintf(&b, "      type %sDeps struct{ /* what it needs, resolved by type */ }\n", x)
			fmt.Fprintf(&b, "      func New%s(d %sDeps) (%s, error) { /* construct it */ }\n\n", x, x, p.Type)
		}
		b.WriteString("  forge resolves the field to g.<X>() by type on the next `forge generate`; an optional dependency can say `// forge:optional-dep` instead.\n")
	}
	return fmt.Errorf("%s", b.String())
}

// providerNameFor suggests a provider name from the Deps field.
func providerNameFor(field string) string {
	if field == "" {
		return "Thing"
	}
	return strings.ToUpper(field[:1]) + field[1:]
}

// lazyProducers returns the producers of T for a field typed func() T.
func (r *graphResolver) lazyProducers(n *graphNode, want types.Type, written string, imports map[string]string) []*graphNode {
	if want != nil {
		if sig, ok := want.Underlying().(*types.Signature); ok && sig.Params().Len() == 0 && sig.Results().Len() == 1 && !sig.Variadic() {
			if cands := r.u.producersOf(sig.Results().At(0).Type(), n.Name, r.nodes); len(cands) > 0 {
				return cands
			}
		}
	}
	inner, ok := strings.CutPrefix(strings.TrimSpace(written), "func() ")
	if !ok || strings.ContainsAny(inner, "(),") {
		return nil
	}
	return r.producersByWrittenType(n, inner, imports)
}

// isGeneratedConfig reports whether q names the config message name in the
// project's generated config package, *<module>/gen/<project>/config/v1
// (internal/configproto).
func (r *graphResolver) isGeneratedConfig(q, name string) bool {
	return strings.HasPrefix(q, "*"+r.module+"/gen/") && strings.HasSuffix(q, "/config/v1."+name)
}
