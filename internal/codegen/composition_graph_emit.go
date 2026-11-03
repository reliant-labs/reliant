// File: internal/codegen/composition_graph_emit.go
//
// GenerateGraph: discover the nodes and roots, reconcile the owned compose.go,
// check the graph for cycles, and emit the forge-owned graph_gen.go (the
// accessors, roots, Build/Run) and routes_gen.go (the per-audience route
// sets). Every shape error — a missing provider, an ambiguous match, a cycle,
// a root field nothing produces — fails generate with the node, field and
// type named, before anything is written.

package codegen

import (
	"fmt"
	"go/format"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/templates"
)

// GraphGenInput is GenerateGraph's input: the discovered component set.
type GraphGenInput struct {
	GenContext
	Services        []ServiceDef
	Packages        []BootstrapPackageData
	Workers         []BootstrapWorkerData
	Operators       []BootstrapOperatorData
	WebhookServices map[string]bool
	// Warnings receives advisory lines; nil is os.Stderr.
	Warnings io.Writer
	// Roots, when set, receives the roots as processes — the reach the
	// deploy config (config_gen.k's ROOTS) projects.
	Roots *[]GraphRoot
}

func (in GraphGenInput) warnings() io.Writer {
	if in.Warnings != nil {
		return in.Warnings
	}
	return os.Stderr
}

// graphImport is one import line of a generated file.
type graphImport struct{ Alias, Path string }

// graphNodeView is a node as graph_gen.go.tmpl renders it.
type graphNodeView struct {
	Name, LogName, Type, Doc string
	HasLifecycle             bool
	LifecycleDoc             string
	NodeFields               string
	NilGuard                 bool
}

type graphRootFieldView struct{ Name, Type, Accessor string }

type graphRootView struct {
	Name, TypeName string
	// Short and Long are the root's own help (graphRoot.Short/Long).
	Short, Long string
	Generated   bool
	Fields      []graphRootFieldView
	ForceOrder  []graphRootFieldView
	// Lazy are the nodes the closure reaches only through a lazy edge (a
	// method value), forced after the fields so they are built at boot.
	Lazy []string
	Plan string
	// Reach is every node the root's closure holds, in start order.
	Reach []string
	// RPCServices is the canonical inventory of route sets in this closure.
	RPCServices []string
	// ServesProbes is true when a node in the closure serves g.Probes (the
	// API listener puts them in front of its edge). A root with none gets a
	// probe listener of its own on the server address, so every process
	// answers /healthz and /readyz.
	ServesProbes bool
	// ReachesDB is true when the closure reaches the database.
	ReachesDB bool
	// MigratePolicy is the root's `migrate=` option as declared ("" when it
	// declares none).
	MigratePolicy string
	// Migrate is true when the process applies the migrations before
	// building the root: migrate=apply, or, by default, a root whose closure
	// reaches the database.
	Migrate bool
	// AwaitMigrations is true when the process instead waits until another
	// has applied them (migrate=wait).
	AwaitMigrations bool
	// Command is the cobra command path that runs the root.
	Command []string
	// PlanNodes is Reach with each node's lifecycle: Plan<Root>().
	PlanNodes []graphPlanNodeView
	// Config is every config field the root reaches; HasConfig is false
	// when the project has no root config message to read reach against.
	Config    []ConfigLeaf
	HasConfig bool
	// Loads are the other configs the root's process loads (a subtree
	// config over its command, its command config), each with the fields of
	// it the root reaches.
	Loads []RootConfigLoad
}

// LoadVar is the local roots_gen.go loads a config into: ApiConfig →
// apiConfig.
func (l RootConfigLoad) LoadVar() string { return strings.ToLower(l.Message[:1]) + l.Message[1:] }

// graphConfigInputView is one config input on app.Graph (graph_gen.go).
type graphConfigInputView struct{ Name, Doc string }

type graphOperatorView struct{ Field, Type, Alias, Label string }

type graphOperatorsView struct {
	LeaderElectionID string
	Ops              []graphOperatorView
}

type graphGenView struct {
	Module    string
	Imports   []graphImport
	Nodes     []graphNodeView
	Roots     []graphRootView
	Operators *graphOperatorsView
	// ProbeListener is true when some root serves no g.Probes of its own,
	// so graph_gen.go carries the probe listener Run<Root> adds for it.
	ProbeListener bool
	// ConfigInputs are the primary binary's configs beside the root config,
	// each an app.Graph field a process that loads it sets.
	ConfigInputs []graphConfigInputView
}

type routesServiceView struct {
	InventoryServiceData
	Type string
}

type routesAudienceView struct {
	Name, Audience string
	Default        bool
	Services       []routesServiceView
}

type routesGenView struct {
	Module         string
	ConnectImports []string
	Imports        []graphImport
	Services       []routesServiceView
	Audiences      []routesAudienceView
}

// GenerateGraph emits the composition graph for a project in graph mode
// (GraphComposition): compose.go reconciled, graph_gen.go and routes_gen.go
// regenerated.
func GenerateGraph(in GraphGenInput) error {
	if err := os.MkdirAll(filepath.Join(in.ProjectDir, "internal", "app"), 0o755); err != nil {
		return err
	}
	m, err := assembleGraph(in)
	if err != nil {
		return err
	}
	for _, w := range staleGraphMethods(m.compose, m.byName) {
		fmt.Fprintf(in.warnings(), "  ⚠️  %s\n", w)
	}
	if w := m.unbuiltWarning(); w != "" {
		fmt.Fprintf(in.warnings(), "  ⚠️  %s\n", w)
	}

	// graph_gen.go
	gv := graphGenViewOf(in.ModulePath, m.built(), m.rootViews, m.opsView, m.opNodes)
	gv.ConfigInputs = m.configs.inputViews()
	graphSrc, err := renderGoTemplate("graph_gen.go.tmpl", gv)
	if err != nil {
		return err
	}
	routesSrc, err := renderGoTemplate("routes_gen.go.tmpl", m.routesView)
	if err != nil {
		return err
	}

	// Write: the generated pair, then the owned compose.go.
	RetireGenerated(in.ProjectDir, filepath.Join("internal", "app", "mounts_services_gen.go"), in.Checksums)
	if err := writeForgeOwned(in.ProjectDir, filepath.Join("internal", "app", "graph_gen.go"), graphSrc, in.Checksums); err != nil {
		return fmt.Errorf("write internal/app/graph_gen.go: %w", err)
	}
	if err := writeForgeOwned(in.ProjectDir, filepath.Join("internal", "app", "routes_gen.go"), routesSrc, in.Checksums); err != nil {
		return fmt.Errorf("write internal/app/routes_gen.go: %w", err)
	}
	if !m.disowned && m.compose != m.existing {
		if err := writeUserScaffold(filepath.Join(in.ProjectDir, "internal", "app", "compose.go"), []byte(m.compose)); err != nil {
			return fmt.Errorf("write internal/app/compose.go: %w", err)
		}
	}
	if in.Roots != nil {
		*in.Roots = graphRoots(m.rootViews)
	}
	if err := emitGraphCatalog(in, m); err != nil {
		return fmt.Errorf("write service catalog: %w", err)
	}
	// cmd/<bin>/cmd/roots_gen.go: one command per root.
	return emitRootCommands(in.ProjectDir, in.ModulePath, m.rootViews, m.groups, m.configs.inputViews(), m.configs.rootMessage(), in.Checksums, in.warnings())
}

// graphModel is the composition graph of one project as generate sees it:
// every node with its type and lifecycle, compose.go reconciled (in memory),
// the edges its value expressions draw, and the roots resolved. GenerateGraph
// renders it; `forge lint --graph` judges it.
type graphModel struct {
	nodes    []*graphNode
	byName   map[string]*graphNode
	compNode map[string]*graphNode // component FieldName → node
	u        *graphUniverse

	// existing is compose.go on disk, compose the reconciled file.
	existing, compose string
	disowned          bool
	edges, lazy       map[string][]string

	// unbuilt are the nodes left out of the emitted graph: no root reaches
	// them and forge cannot fill their Deps (composition_graph_reach.go).
	unbuilt map[string]unbuiltNode

	roots     []graphRoot
	rootViews []graphRootView
	// groups is the group commands' declared help, by command path.
	groups map[string]groupHelp
	// configs is every config the processes load (nil: no root config).
	configs *processConfigs

	routesView routesGenView
	opsView    *graphOperatorsView
	opNodes    []*graphNode
}

// assembleGraph discovers, types, reconciles and resolves the graph, writing
// nothing. Every shape error — a missing provider, an ambiguous match, a
// cycle, a root field nothing produces — is returned.
func assembleGraph(in GraphGenInput) (*graphModel, error) {
	inject := InjectGenInput{GenContext: in.GenContext, Services: in.Services, Packages: in.Packages, Workers: in.Workers, Operators: in.Operators, Graph: true}
	comps, err := assembleBuildComponents(inject)
	if err != nil {
		return nil, err
	}
	comps, notComponents := composedComponents(in.ProjectDir, comps)
	if consumed := notAComponentConsumers(comps, notComponents); len(consumed) > 0 {
		return nil, notAComponentConsumedError(in.ProjectDir, consumed)
	}
	if bad := unconstructibleComponents(in.ProjectDir, comps); len(bad) > 0 {
		return nil, unconstructibleError(bad)
	}
	providers, roots, groups, err := discoverAppDeclsAndGroups(in.ProjectDir)
	if err != nil {
		return nil, err
	}

	// Nodes: components, providers, then the generated ones.
	m := &graphModel{byName: map[string]*graphNode{}, compNode: map[string]*graphNode{}, groups: groups}
	add := func(n *graphNode) error {
		if prev, dup := m.byName[n.Name]; dup {
			return fmt.Errorf("the composition graph has two nodes named %s (%s and %s): every node needs its own accessor g.%s() — rename the provider func", n.Name, prev.Doc, n.Doc, n.Name)
		}
		if reservedGraphNames[n.Name] {
			return fmt.Errorf("a node named %s (%s) would collide with app.Graph's own %s — rename it", n.Name, n.Doc, n.Name)
		}
		m.byName[n.Name] = n
		m.nodes = append(m.nodes, n)
		return nil
	}
	for i := range comps {
		c := &comps[i]
		n := componentNode(in.ModulePath, c)
		m.compNode[c.FieldName] = n
		if err := add(n); err != nil {
			return nil, err
		}
	}
	if err := addGraphProviderNodes(providers, add); err != nil {
		return nil, err
	}
	if m.routesView, err = addGraphRouteNodes(in, m.compNode, add); err != nil {
		return nil, err
	}
	if m.opsView, m.opNodes, err = addGraphOperatorsNode(in.ModulePath, m.nodes, add); err != nil {
		return nil, err
	}

	// Config inputs: each of the primary binary's configs beside the root
	// config is an app.Graph field, so no node may take its name.
	m.configs = loadProcessConfigs(in.ProjectDir)
	if m.configs != nil {
		// What the owned process policy reads is every root's reach too.
		m.configs.process = runRootConfigReads(in.ProjectDir, m.configs.configInputs())
	}
	for name := range m.configs.configInputs() {
		if n := m.byName[name]; n != nil {
			return nil, fmt.Errorf("a node named %s (%s) would collide with app.Graph's %s, the config proto/<project>/config/v1's %s message binds to the primary binary — rename the node", name, n.Doc, name, name)
		}
		if reservedGraphNames[name] {
			return nil, fmt.Errorf("config message %s is bound to the primary binary, so it is an app.Graph input of that name — which app.Graph already declares itself: rename the message", name)
		}
	}

	// Types: what each node produces, its Deps field types, its lifecycle.
	m.u = loadGraphUniverse(in.ProjectDir, comps)
	m.u.typeNodes(m.nodes)
	for _, n := range m.nodes {
		if n.Kind == nodeRoutes || n.Kind == nodeOperators {
			continue
		}
		if !n.NilGuard && (strings.HasPrefix(n.Type, "*") || n.Comp != nil && !strings.HasPrefix(n.Type, "*")) {
			// Untyped (mid-edit): a pointer, or a component's contract
			// interface, can be nil.
			n.NilGuard = true
		}
	}

	resolver := &graphResolver{
		module:       in.ModulePath,
		nodes:        m.nodes,
		byName:       m.byName,
		u:            m.u,
		blocks:       parseConfigBlocks(in.ProjectDir, in.ModulePath),
		configFields: parseConfigFields(in.ProjectDir),
		store:        parseStoreSeam(in.ProjectDir),
		configInputs: m.configs.configInputs(),
	}

	if err := m.wire(in, resolver, roots); err != nil {
		return nil, err
	}
	return m, nil
}

// wire reconciles compose.go against the typed nodes (or, when disowned,
// reads it as is), reads the edges its value expressions draw, and resolves
// the roots over them: declared, else the default. A node no root reaches
// whose Deps do not resolve is left out (composition_graph_reach.go).
func (m *graphModel) wire(in GraphGenInput, resolver *graphResolver, roots []graphRoot) error {
	scope := func(draft string, problems []graphProblem) (map[string]unbuiltNode, error) {
		return m.outOfReach(roots, draft, problems)
	}
	f, err := reconcileGraphComposeFile(in, m.nodes, resolver, scope)
	if err != nil {
		return err
	}
	m.existing, m.compose, m.disowned, m.unbuilt = f.existing, f.compose, f.disowned, f.unbuilt
	built := m.built()
	m.edges, m.lazy = graphComposeEdgesLazy(m.compose, built)
	if cycle := graphCycle(built, m.edges); cycle != nil {
		return graphCycleError(cycle)
	}

	// Roots.
	m.roots = roots
	if len(m.roots) == 0 {
		m.roots = []graphRoot{defaultGraphRoot(built, m.edges)}
	}
	if err := checkRootCommands(m.roots); err != nil {
		return err
	}
	if err := checkGroupHelp(m.groups, m.roots); err != nil {
		return err
	}
	if m.rootViews, err = graphRootViews(m.roots, built, m.u, m.edges, m.lazy); err != nil {
		return err
	}
	if err := checkRootOnlyReach(m.rootViews, built, m.edges); err != nil {
		return err
	}
	markRootReach(m.rootViews, graphComposeInputReads(m.compose, built), m.byName, m.u)
	return markProcessConfig(m.rootViews, m.configs, graphComposeInputConfigReads(m.compose, built, m.configs.inputNames()))
}

// addGraphProviderNodes adds one node per provider func an owned
// internal/app file declares.
func addGraphProviderNodes(providers []graphProvider, add func(*graphNode) error) error {
	for _, p := range providers {
		typeImports, unknown := typeExprImports(p.Result, p.Imports)
		if len(unknown) > 0 {
			return fmt.Errorf("%s: New%s returns %s, whose package %q the file does not import", p.File, p.Name, p.Result, unknown[0])
		}
		n := &graphNode{
			Name: p.Name, LogName: logName(p.Name), Kind: nodeProvider,
			Doc:  fmt.Sprintf("the %s provider (New%s, %s)", p.Name, p.Name, p.File),
			Type: p.Result, Imports: typeImports,
			Ctor: "New" + p.Name, DepsType: p.Name + "Deps", Fallible: p.Fallible,
			Deps: p.Deps, DepsKeys: p.DepsKeys, DepsFound: true,
			SrcImports: p.Imports, Pos: p.Pos,
			File: p.File, Line: p.Line, Standby: p.Standby, AuthAudience: p.Audience,
			RootOnly: p.RootOnly,
		}
		if err := add(n); err != nil {
			return err
		}
	}
	return nil
}

// graphRootViews resolves each root's fields against the nodes and renders
// it, refusing two roots of one name.
func graphRootViews(roots []graphRoot, nodes []*graphNode, u *graphUniverse, edges, lazy map[string][]string) ([]graphRootView, error) {
	var rootViews []graphRootView
	seenRoot := map[string]string{}
	for i := range roots {
		r := &roots[i]
		if prev, dup := seenRoot[r.Name]; dup {
			return nil, fmt.Errorf("%s:%d: two roots are named %q (%s and %s)", r.File, r.Line, r.Name, prev, r.TypeName)
		}
		seenRoot[r.Name] = r.TypeName
		if !r.Generated {
			if err := resolveRootFields(r, nodes, u); err != nil {
				return nil, err
			}
		}
		v := rootViewLazy(r, edges, lazy)
		// Only route-set membership proves a handler is mounted. A worker
		// may reach the same handler as an in-process dependency.
		seenRPC := map[string]bool{}
		for _, name := range v.Reach {
			for _, n := range nodes {
				if n.Name != name || n.Kind != nodeRoutes {
					continue
				}
				for _, svc := range n.Services {
					if svc != nil && svc.RPCService != "" {
						seenRPC[svc.RPCService] = true
					}
				}
			}
		}
		for rpc := range seenRPC {
			v.RPCServices = append(v.RPCServices, rpc)
		}
		sort.Strings(v.RPCServices)
		v.Command = r.Command
		v.MigratePolicy = r.Migrate
		rootViews = append(rootViews, v)
	}
	return rootViews, nil
}

// graphGenViewOf is graph_gen.go's view: every node, the roots, the
// operators, and the imports their types need.
func graphGenViewOf(module string, nodes []*graphNode, rootViews []graphRootView, opsView *graphOperatorsView, opNodes []*graphNode) graphGenView {
	byName := make(map[string]*graphNode, len(nodes))
	for _, n := range nodes {
		byName[n.Name] = n
	}
	gv := graphGenView{Module: module, Roots: make([]graphRootView, len(rootViews)), Operators: opsView}
	for i, rv := range rootViews {
		if !rv.ServesProbes {
			gv.ProbeListener = true
		}
		rv.PlanNodes = planNodes(rv, byName)
		gv.Roots[i] = rv
	}
	nodeImports := map[string]string{}
	for _, n := range nodes {
		for a, p := range n.Imports {
			nodeImports[a] = p
		}
		gv.Nodes = append(gv.Nodes, nodeView(n))
	}
	if opsView != nil {
		for _, op := range opNodes {
			nodeImports[op.Comp.Alias] = module + "/" + op.Comp.ImportPath
		}
	}
	gv.Imports = sortedImports(nodeImports, "context", "fmt", "log/slog", "strings", "github.com/reliant-labs/forge/pkg/appkit/operatorkit",
		"github.com/reliant-labs/forge/pkg/graphkit", "github.com/reliant-labs/forge/pkg/lifecyclekit",
		"github.com/reliant-labs/forge/pkg/serverkit", module+"/pkg/config")
	return gv
}

// reconcileGraphComposeFile reads internal/app/compose.go and, unless it is
// disowned, reconciles it against nodes. It returns the file as read, the
// reconciled source, and whether it is disowned (then the two are equal).
//
// The reconcile measures only the nodes' own constructions. compose.go is
// owned and only ever added to, so it can keep constructing a project package
// that is no longer a node — marked //forge:external-component or
// //forge:not-a-component, or out of discovery altogether. Go still fixes that
// literal's keys to the package's Deps struct, so a removed field breaks the
// build there exactly as it would for a node (#747): those keys are dropped
// too, and the construction itself is reported, never deleted.
//
// A key that does not resolve goes to scope with the draft the resolved keys
// make: scope returns the nodes to leave out — none of them reached by a root
// — or the error to fail with, and the file is reconciled again without them.
// A nil scope fails on any problem.
func reconcileGraphComposeFile(in GraphGenInput, nodes []*graphNode, resolver *graphResolver, scope reachScope) (graphComposeFile, error) {
	composeRel := filepath.Join("internal", "app", "compose.go")
	composeAbs := filepath.Join(in.ProjectDir, composeRel)
	raw, rerr := os.ReadFile(composeAbs)
	if rerr != nil && !os.IsNotExist(rerr) {
		return graphComposeFile{}, fmt.Errorf("read internal/app/compose.go: %w", rerr)
	}
	f := graphComposeFile{existing: string(raw)}
	if disownedFilePresent(in.Checksums, composeRel, composeAbs) {
		f.compose, f.disowned = f.existing, true
		return f, nil
	}
	out, problems, err := reconcileGraphCompose(f.existing, nodes, resolver)
	if err != nil {
		return graphComposeFile{}, err
	}
	if len(problems) > 0 {
		if scope == nil {
			return graphComposeFile{}, graphProblemsError(problems)
		}
		if f.unbuilt, err = scope(out, problems); err != nil {
			return graphComposeFile{}, err
		}
		// The same resolver over fewer nodes: every node kept resolved in
		// the draft, and resolves the same way again.
		if out, problems, err = reconcileGraphCompose(f.existing, withoutNodes(nodes, f.unbuilt), resolver); err != nil {
			return graphComposeFile{}, err
		}
		if len(problems) > 0 {
			return graphComposeFile{}, graphProblemsError(problems)
		}
	}
	stale := graphStaleConstructionData(in, nodes)
	out, _ = composeDropStaleDepsKeys(out, stale)
	for _, w := range composeStaleConstructions(out, stale) {
		fmt.Fprintf(in.warnings(), "  ⚠️  %s\n", w)
	}
	f.compose = out
	return f, nil
}

// graphComposeFile is compose.go as generate reconciles it: the file as read,
// the reconciled source (equal when disowned), and the nodes left out.
type graphComposeFile struct {
	existing, compose string
	disowned          bool
	unbuilt           map[string]unbuiltNode
}

// graphStaleConstructionData is the view composeDropStaleDepsKeys and
// composeStaleConstructions take of the graph: the component nodes, with the
// Deps keys each one's literal is measured against.
func graphStaleConstructionData(in GraphGenInput, nodes []*graphNode) InjectGenData {
	data := InjectGenData{Module: in.ModulePath, ProjectDir: in.ProjectDir, Graph: true}
	for _, n := range nodes {
		if n.Comp == nil {
			continue
		}
		data.Order = append(data.Order, InjectComponentData{
			Alias: n.Comp.Alias, ImportPath: n.Comp.ImportPath,
			DepsFound: n.DepsFound, DepsKeys: n.DepsKeys,
		})
	}
	return data
}

// addGraphRouteNodes adds one route-set node per audience the services
// declare (the user audience always), and returns routes_gen.go's view.
func addGraphRouteNodes(in GraphGenInput, compNode map[string]*graphNode, add func(*graphNode) error) (routesGenView, error) {
	rows, connectImports, err := inventoryRows(InventoryGenInput{GenContext: in.GenContext, Services: in.Services, Packages: in.Packages, Workers: in.Workers, Operators: in.Operators, WebhookServices: in.WebhookServices})
	if err != nil {
		return routesGenView{}, err
	}
	routesView := routesGenView{Module: in.ModulePath, ConnectImports: connectImports}
	routesImports := map[string]string{}
	byAudience := map[string][]routesServiceView{"user": nil}
	for _, row := range rows {
		cn := compNode[row.FieldName]
		if cn == nil {
			continue // a not-a-component handler: nothing to mount
		}
		for _, svc := range in.Services {
			if svc.Name == row.ProtoServiceName {
				cn.RPCService = svc.Package + "." + svc.Name
				break
			}
		}
		sv := routesServiceView{InventoryServiceData: row, Type: cn.Type}
		routesView.Services = append(routesView.Services, sv)
		byAudience[row.Audience] = append(byAudience[row.Audience], sv)
		for a, p := range cn.Imports {
			routesImports[a] = p
		}
	}
	for _, a := range sortedAudiences(byAudience) {
		name := audienceRoutesName(a)
		av := routesAudienceView{Name: name, Audience: a, Default: a == "user", Services: byAudience[a]}
		routesView.Audiences = append(routesView.Audiences, av)
		if err := add(routesNode(av, routesImports, compNode)); err != nil {
			return routesGenView{}, err
		}
	}
	routesView.Imports = sortedImports(routesImports, "fmt", "log/slog", "net/http", "strings", "connectrpc.com/connect",
		"github.com/reliant-labs/forge/pkg/middleware", "github.com/reliant-labs/forge/pkg/mountkit",
		"github.com/reliant-labs/forge/pkg/mountkit/inventory", "github.com/reliant-labs/forge/pkg/authn")
	return routesView, nil
}

// sortedAudiences is byAudience's keys, the user audience first.
func sortedAudiences(byAudience map[string][]routesServiceView) []string {
	audiences := make([]string, 0, len(byAudience))
	for a := range byAudience {
		audiences = append(audiences, a)
	}
	sort.Slice(audiences, func(i, j int) bool {
		if (audiences[i] == "user") != (audiences[j] == "user") {
			return audiences[i] == "user"
		}
		return audiences[i] < audiences[j]
	})
	return audiences
}

// routesNode is one audience's route set as a graph node: a Deps field per
// service it mounts, plus Logger and Options.
func routesNode(av routesAudienceView, routesImports map[string]string, compNode map[string]*graphNode) *graphNode {
	n := &graphNode{
		Name: av.Name, LogName: logName(av.Name), Kind: nodeRoutes, Audience: av.Audience,
		Doc:  fmt.Sprintf("the %s-audience route set (routes_gen.go)", av.Audience),
		Type: "*" + av.Name, Ctor: "New" + av.Name, DepsType: av.Name + "Deps", Fallible: true,
		DepsKeys: map[string]bool{"Logger": true, "Options": true}, DepsFound: true,
		SrcImports: routesImports,
	}
	for _, sv := range av.Services {
		n.Deps = append(n.Deps, DepsField{Name: sv.FieldName, Type: sv.Type})
		n.DepsKeys[sv.FieldName] = true
		n.Services = append(n.Services, compNode[sv.FieldName])
	}
	n.Deps = append(n.Deps, DepsField{Name: "Logger", Type: "*slog.Logger"}, DepsField{Name: "Options", Type: "MountOptions"})
	return n
}

// addGraphOperatorsNode adds the controller-manager node when the project has
// operators, and returns graph_gen.go's view of it and the operator nodes.
func addGraphOperatorsNode(module string, nodes []*graphNode, add func(*graphNode) error) (*graphOperatorsView, []*graphNode, error) {
	var opNodes []*graphNode
	for _, n := range nodes {
		if n.Role == "internal/operators" {
			opNodes = append(opNodes, n)
		}
	}
	if len(opNodes) == 0 {
		return nil, nil, nil
	}
	opsView := &graphOperatorsView{LeaderElectionID: leaderElectionID(module)}
	n := &graphNode{
		Name: "Operators", LogName: "operators", Kind: nodeOperators,
		Doc:  "the controller manager running every operator (graph_gen.go)",
		Type: "*Operators", Ctor: "NewOperators", DepsType: "OperatorsDeps", Fallible: true,
		DepsKeys: map[string]bool{"Logger": true, "ServerCfg": true}, DepsFound: true,
		Lifecycle: lifecycle{Run: "Run"}, NilGuard: true,
		SrcImports: map[string]string{},
	}
	for _, op := range opNodes {
		n.Deps = append(n.Deps, DepsField{Name: op.Name, Type: op.Type})
		n.DepsKeys[op.Name] = true
		for a, p := range op.Imports {
			n.SrcImports[a] = p
		}
		opsView.Ops = append(opsView.Ops, graphOperatorView{Field: op.Name, Type: op.Type, Alias: op.Comp.Alias, Label: op.Comp.Name})
	}
	n.Deps = append(n.Deps, DepsField{Name: "Logger", Type: "*slog.Logger"}, DepsField{Name: "ServerCfg", Type: "serverkit.Config"})
	if err := add(n); err != nil {
		return nil, nil, err
	}
	return opsView, opNodes, nil
}

// reservedGraphNames are app.Graph's own members, which no node may shadow.
var reservedGraphNames = map[string]bool{
	"Ctx": true, "Cfg": true, "Log": true, "ServerCfg": true, "Probes": true, "Built": true,
}

// componentNode is a discovered component as a graph node.
func componentNode(module string, c *BuildComponent) *graphNode {
	importPath := module + "/" + c.ImportPath
	typ := c.compFieldType
	if typ == "" {
		typ = "*" + c.Alias + ".Service"
	}
	ctor := c.compConstructor
	if ctor == "" {
		ctor = DefaultConstructorName
	}
	role := map[string]string{
		"internal/handlers":  "handler",
		"internal":           "package",
		"internal/workers":   "worker",
		"internal/operators": "operator",
	}[c.compRoleRoot]
	if role == "" {
		role = "component"
	}
	return &graphNode{
		Name: c.FieldName, LogName: logName(c.FieldName), Kind: nodeComponent,
		Doc:  fmt.Sprintf("the %s %s (%s)", c.Name, role, c.ImportPath),
		Type: typ, Imports: map[string]string{c.Alias: importPath},
		Ctor: c.Alias + "." + ctor, DepsType: c.Alias + ".Deps", DepsImport: importPath,
		Fallible: c.compFallible, Wrapped: c.compWrapped, MiddlewareCtor: c.compMiddlewareCtor,
		Deps: c.Deps, DepsKeys: c.DepsKeys, DepsFound: c.DepsFound,
		Comp: c, Role: c.compRoleRoot,
	}
}

// nodeView renders a node for graph_gen.go.
func nodeView(n *graphNode) graphNodeView {
	v := graphNodeView{Name: n.Name, LogName: n.LogName, Type: n.Type, Doc: n.Doc, NilGuard: n.NilGuard}
	l := n.Lifecycle
	if !l.any() {
		return v
	}
	v.HasLifecycle = true
	var fields, doc []string
	for _, f := range []struct{ phase, method string }{{"Run", l.Run}, {"Drain", l.Drain}, {"Close", l.Close}, {"Ready", l.Ready}} {
		if f.method == "" {
			continue
		}
		fields = append(fields, f.phase+": c."+f.method)
		if f.phase == f.method {
			doc = append(doc, f.phase)
		} else {
			doc = append(doc, f.phase+" ("+f.method+")")
		}
	}
	v.NodeFields = strings.Join(fields, ", ")
	v.LifecycleDoc = strings.Join(doc, ", ")
	return v
}

// defaultGraphRoot is the generated root when none is declared: every node
// with a Run method that no other node depends on, providers first (in
// declaration order), then workers, then the operators' manager.
func defaultGraphRoot(nodes []*graphNode, edges map[string][]string) graphRoot {
	consumed := map[string]bool{}
	for _, ds := range edges {
		for _, d := range ds {
			consumed[d] = true
		}
	}
	rank := func(n *graphNode) int {
		switch {
		case n.Kind == nodeProvider:
			return 0
		case n.Role == "internal/workers":
			return 2
		case n.Kind == nodeOperators:
			return 3
		}
		return 1
	}
	var sinks []*graphNode
	for _, n := range nodes {
		if n.Lifecycle.Run != "" && !consumed[n.Name] {
			sinks = append(sinks, n)
		}
	}
	sort.SliceStable(sinks, func(i, j int) bool {
		if rank(sinks[i]) != rank(sinks[j]) {
			return rank(sinks[i]) < rank(sinks[j])
		}
		if sinks[i].Kind == nodeProvider {
			return sinks[i].Pos < sinks[j].Pos
		}
		return sinks[i].Name < sinks[j].Name
	})
	r := graphRoot{Name: "server", TypeName: "Server", Generated: true, Command: []string{"server"}}
	for _, n := range sinks {
		r.Fields = append(r.Fields, graphRootField{Name: n.Name, Type: n.Type, Node: n})
	}
	return r
}

// resolveRootFields resolves each field of a declared root to the node it
// holds, by type.
func resolveRootFields(r *graphRoot, nodes []*graphNode, u *graphUniverse) error {
	for i := range r.Fields {
		f := &r.Fields[i]
		cands := u.producersOf(u.rootFieldType(r.TypeName, f.Name), "", nodes)
		if len(cands) == 0 {
			for _, n := range nodes {
				if n.Type == f.Type {
					cands = append(cands, n)
				}
			}
		}
		switch len(cands) {
		case 1:
			f.Node = cands[0]
		case 0:
			return fmt.Errorf("%s:%d: root %s field %s (%s): no node produces that type — add a provider func `func New<X>(<X>Deps) (%s, error)` to internal/app, or type the field as a node's own type", r.File, r.Line, r.TypeName, f.Name, f.Type, f.Type)
		default:
			names := make([]string, len(cands))
			for j, c := range cands {
				names[j] = c.Name + " (" + c.Type + ")"
			}
			return fmt.Errorf("%s:%d: root %s field %s (%s) could be any of %s — type the field as exactly one node's type (instance identity is the Go type)", r.File, r.Line, r.TypeName, f.Name, f.Type, strings.Join(names, ", "))
		}
	}
	return nil
}

// rootView renders a root: its fields, the order Build forces them in, and
// the start order its closure gets, from compose.go's edges.
func rootView(r *graphRoot, edges map[string][]string) graphRootView {
	return rootViewLazy(r, edges, nil)
}

// rootViewLazy is rootView for a graph with lazy edges (a method value
// g.X in compose.go): Build also forces every node its closure reaches
// lazily, so a lazily-used node is constructed at boot, never on first use.
func rootViewLazy(r *graphRoot, edges, lazy map[string][]string) graphRootView {
	v := graphRootView{Name: r.Name, TypeName: r.TypeName, Short: r.Short, Long: r.Long, Generated: r.Generated}
	for _, f := range r.Fields {
		v.Fields = append(v.Fields, graphRootFieldView{Name: f.Name, Type: f.Type, Accessor: f.Node.Name})
	}
	for i := len(v.Fields) - 1; i >= 0; i-- {
		v.ForceOrder = append(v.ForceOrder, v.Fields[i])
	}
	// The same walk Build makes: fields last to first, each node's
	// dependencies (in compose.go's order) before it.
	var order []string
	done := map[string]bool{}
	logNameToNode := map[string]string{}
	var visit func(string)
	visit = func(n string) {
		if done[n] {
			return
		}
		done[n] = true
		for _, d := range edges[n] {
			visit(d)
		}
		logNameToNode[logName(n)] = n
		order = append(order, logName(n))
	}
	for _, f := range v.ForceOrder {
		visit(f.Accessor)
	}
	// Lazy targets of the closure, until nothing new is reached.
	for j := 0; j < len(order); j++ {
		for _, l := range lazy[logNameToNode[order[j]]] {
			if !done[l] {
				v.Lazy = append(v.Lazy, l)
				visit(l)
			}
		}
	}
	for _, ln := range order {
		v.Reach = append(v.Reach, logNameToNode[ln])
	}
	if len(order) == 0 {
		v.Plan = "(nothing)"
	} else {
		v.Plan = strings.Join(order, " → ")
	}
	return v
}

// graphCycleError names a dependency cycle by its edges.
func graphCycleError(cycle []string) error {
	var steps []string
	for i := 0; i+1 < len(cycle); i++ {
		steps = append(steps, fmt.Sprintf("new%s (compose.go) calls g.%s()", cycle[i], cycle[i+1]))
	}
	return fmt.Errorf("the composition graph has a dependency cycle: %s\n\n  %s\n\nA node cannot be constructed before itself. Break one edge by making it lazy: type that Deps field as a func returning the dependency (`Peer func() *peer.Service`). forge wires it to the accessor's method value — `Peer: g.Peer,`, no call — so it is resolved on first use, after both nodes exist, and draws no construction edge (an existing key is yours: change its value to the method value yourself)",
		strings.Join(cycle, " → "), strings.Join(steps, "\n  "))
}

// staleGraphMethods lists compose.go constructor methods for nodes the
// project no longer has.
func staleGraphMethods(compose string, byName map[string]*graphNode) []string {
	var out []string
	for _, x := range composeMethodNames(compose) {
		if _, ok := byName[x]; !ok {
			out = append(out, fmt.Sprintf("internal/app/compose.go: new%s constructs a node the project no longer has — delete the method", x))
		}
	}
	sort.Strings(out)
	return out
}

// renderGoTemplate renders a project template and gofmts it.
func renderGoTemplate(name string, data any) ([]byte, error) {
	src, err := templates.ProjectTemplates().Render(name, data)
	if err != nil {
		return nil, fmt.Errorf("render %s: %w", name, err)
	}
	out, err := format.Source(src)
	if err != nil {
		return nil, fmt.Errorf("format %s (a forge bug — please report it): %w\n%s", name, err, src)
	}
	return out, nil
}

// sortedImports is m as import lines, minus the paths the template imports
// itself.
func sortedImports(m map[string]string, fixed ...string) []graphImport {
	skip := map[string]bool{}
	for _, p := range fixed {
		skip[p] = true
	}
	out := make([]graphImport, 0, len(m))
	for a, p := range m {
		if skip[p] {
			continue
		}
		out = append(out, graphImport{Alias: a, Path: p})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Path < out[j].Path || out[i].Path == out[j].Path && out[i].Alias < out[j].Alias
	})
	return out
}

// providersGraphData is providers-graph.go.tmpl's input.
type providersGraphData struct {
	Module                   string
	HasDatabase, RESTEnabled bool
	ConfigFields             map[string]bool
	// AuthConfigType and BrokerConfigType are the pkg/config types auth.go
	// and login_broker.go read (configDeclaring; "" is Config), and
	// NoBrokerConfig is true when no config declares the broker's fields.
	AuthConfigType, BrokerConfigType string
	NoBrokerConfig                   bool
	// NoProjectAuthInterceptor: the project's pkg/middleware declares no
	// NewAuthInterceptor (projectDeclaresAuthInterceptor).
	NoProjectAuthInterceptor bool
}

// projectAuthInterceptorDecl matches the auth interceptor the scaffolded
// pkg/middleware/middleware.go declares.
var projectAuthInterceptorDecl = regexp.MustCompile(`(?m)^func NewAuthInterceptor\(`)

// projectDeclaresAuthInterceptor reports whether the project's pkg/middleware
// declares NewAuthInterceptor — forge's scaffold does, a project with a
// middleware package of its own may not, and providers.go's UserAuth then
// builds forge's authn interceptor instead of calling a function that is not
// there. A project with no pkg/middleware yet is about to get forge's.
func projectDeclaresAuthInterceptor(projectDir string) bool {
	dir := filepath.Join(projectDir, "pkg", "middleware")
	entries, err := os.ReadDir(dir)
	if err != nil {
		return true
	}
	scaffolded := false
	for _, e := range entries {
		n := e.Name()
		if e.IsDir() || !strings.HasSuffix(n, ".go") || strings.HasSuffix(n, "_test.go") {
			continue
		}
		if !strings.HasSuffix(n, "_gen.go") {
			scaffolded = true
		}
		if src, err := os.ReadFile(filepath.Join(dir, n)); err == nil && projectAuthInterceptorDecl.Match(src) {
			return true
		}
	}
	return !scaffolded
}

// GenerateGraphProviders scaffolds a graph-mode project's owned
// internal/app/providers.go (the default providers: the database pool and
// ORM, the outbound HTTP client, the user audience's authenticator, chain and
// API listener, pprof) and auth.go, each exactly once.
func GenerateGraphProviders(modulePath, databaseDriver, projectDir string) error {
	hasDB := databaseDriver != ""
	// auth.go and login_broker.go read the config that declares their
	// fields — a command config's, in a binary_config project — or none.
	authConfig, hasJWT := configDeclaring(projectDir, jwtConfigFields)
	brokerConfig, hasBroker := configDeclaring(projectDir, brokerConfigFields)
	if err := GenerateAuthSetupData(projectDir, AuthSetupTemplateData{Module: modulePath, Graph: true, HasDatabase: hasDB, AuthConfigType: authConfig, NoJWTConfig: !hasJWT}); err != nil {
		return err
	}
	rel := filepath.Join("internal", "app", "providers.go")
	configFields := DefaultConfigFieldNames()
	if msgs, err := ParseConfigProtosFromDir(filepath.Join(projectDir, "proto", "config")); err == nil && len(msgs) > 0 {
		configFields = RootConfigFieldNames(msgs, projectDir)
	}
	data := providersGraphData{Module: modulePath, HasDatabase: hasDB, RESTEnabled: projectAPIRESTEnabled(projectDir), ConfigFields: configFields,
		AuthConfigType: authConfig, BrokerConfigType: brokerConfig, NoBrokerConfig: !hasBroker,
		NoProjectAuthInterceptor: !projectDeclaresAuthInterceptor(projectDir)}
	content, err := templates.ProjectTemplates().Render("providers-graph.go.tmpl", data)
	if err != nil {
		return fmt.Errorf("render providers-graph.go.tmpl: %w", err)
	}
	if _, err := writeForgeScaffoldOnce(projectDir, rel, content); err != nil {
		return fmt.Errorf("write internal/app/providers.go: %w", err)
	}
	return nil
}
