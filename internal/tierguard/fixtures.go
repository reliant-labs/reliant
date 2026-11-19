package tierguard

// The two fixture project shapes the differential render compares, plus
// the render driver. Kept out of the test file so the shapes read as a
// specification of "materially different user inputs" rather than as
// test scaffolding.

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/cli"
	"github.com/reliant-labs/forge/internal/configproto"

	"github.com/reliant-labs/forge/internal/atomicfile"
)

// FixedProjectName and FixedModulePath are shared by the A and B
// fixtures on purpose — see the "held constant" section of the package
// doc. Project identity is not a derivation input; varying it between A
// and B would make every Go file's import block differ and manufacture
// false "derived" verdicts.
const (
	FixedProjectName = "tgproj"
	FixedModulePath  = "example.com/tgproj"
)

// AltProjectName and AltModulePath belong to the third render (fixture
// C), which carries the SAME user inputs as A and differs ONLY in
// project identity. Comparing A against C isolates a property that A vs
// B cannot see: whether a file embeds the user's module path.
//
// That distinction changes the remedy for a constant file, so it is
// worth a render rather than a judgment call:
//
//   - constant across A/B AND across A/C → plain library code. It can
//     move to forge/pkg essentially verbatim.
//   - constant across A/B but differing across A/C → the file names the
//     user's own module (importing <mod>/internal/app, aliasing
//     <mod>/gen/<project>/config/v1). Its BODY is still not derived from any
//     declaration, so Tier-1 still buys nothing, but it cannot be a
//     verbatim forge/pkg file: the fix is library code in forge/pkg plus
//     the short user-owned scaffold that does the module-specific wiring.
const (
	AltProjectName = "tgother"
	AltModulePath  = "example.org/tgother"
)

// entityMessage is one `// forge:entity` proto message to append to a
// service proto. Fields are written verbatim, so a fixture controls the
// exact scalar kinds the schema (and therefore the ORM, CRUD handlers,
// and seed graph) is derived from.
type entityMessage struct {
	Name   string
	Fields []string
}

// internalPackage is an internal/<Name>/ package created with
// `forge package new`, which lands a contract.go — derivation input #3,
// the one that drives mock and middleware generation.
type internalPackage struct {
	Name string
}

// fixture is one complete set of user inputs: everything a user would
// have authored before running `forge generate`.
type fixture struct {
	// Label names the fixture in failure output.
	Label string
	// Name and Module are the project identity. A and B share one
	// identity (so identity is not mistaken for derivation); C varies it
	// while holding the inputs equal to A.
	Name   string
	Module string
	// Services are the service names, in scaffold order. The first is
	// passed to `project new --service`; the rest are added with
	// `forge scaffold service`.
	Services []string
	// Entities maps a service name to the entity messages declared in
	// its proto. Each becomes a migration + table + CRUD quintet.
	Entities map[string][]entityMessage
	// Frontends are `--frontend` names (forge.yaml shape + features).
	Frontends []string
	// GatewayHTTPPort, when non-zero, moves the scaffolded `public` Gateway's
	// http listener off its default (18080) in deploy/kcl/ingress.k.
	//
	// This is the input deploy/k3d-ports.yaml projects. Since `ingress` derives
	// from a forge.Gateway being declared in deploy/kcl, every scaffolded
	// project emits that file, and it is a function of the listener ports the
	// user's KCL declares. A fixture set where nobody moves a listener leaves
	// it byte-identical and indistinguishable from library code.
	GatewayHTTPPort int
	// InternalPackages each contribute a contract.go.
	InternalPackages []internalPackage
	// PublicRPCs maps a service name to RPCs declared with
	// `auth_required: false`. These are the input that
	// pkg/middleware/procedures_gen.go projects: without at least one
	// fixture declaring a public RPC, that file renders the same
	// two health-check entries either way and looks constant while
	// really only being unexercised.
	PublicRPCs map[string][]string
	// ConfigBlocks are extra component config messages composed onto
	// AppConfig in proto/<project>/config/v1/config.proto — part of derivation
	// input #1, and the one that pkg/config/config.go tracks. AppConfig's
	// SHAPE is user-authored, so a fixture that never touches it leaves
	// the config surface unexercised.
	ConfigBlocks []configBlock
	// Binaries are `forge scaffold binary` names: extra long-running
	// processes, each with its own cmd/<name>. A binary is the ANCHOR a
	// BinaryConfig needs — a config message naming a binary with no
	// cmd/<name> is a generate-time error, not a silently unused config.
	Binaries []string
	// BinaryConfigs are config messages bound to a binary with
	// `(forge.v1.binary_config) = {binary: "<name>"}`. This is the input
	// pkg/config/config_gen.go's per-binary section is projected from:
	// the file emits a Config alias plus Register/Load/ModeOf/Validate
	// FOR EACH ONE. A fixture set where no project declares one leaves
	// that whole half of the template unexercised, and the file then
	// looks constant while really only being untested.
	BinaryConfigs []binaryConfigBlock
}

// binaryConfigBlock is one config message bound to a binary — the
// documented way a binary declares a config surface only its own process
// loads.
type binaryConfigBlock struct {
	// Binary is the cmd/<name> leaf whose process loads this config. It
	// must name a binary the fixture also scaffolds.
	Binary string
	// Name is the message name, e.g. "Indexer" → message IndexerConfig.
	Name string
	// Fields are annotated proto field declarations, written verbatim.
	Fields []string
}

// configBlock is one `<Name>Config` message composed onto AppConfig as a
// field, the documented way a component declares its own configuration.
type configBlock struct {
	// Name is the message name, e.g. "Ledger" → message LedgerConfig.
	Name string
	// Fields are annotated proto field declarations, written verbatim.
	Fields []string
}

// projectA is the smaller shape: ONE service, ONE entity of four
// columns over three scalar kinds, no frontend, and no contract.go.
func projectA() fixture {
	return fixture{
		Label:    "A(1 svc, 1 entity, no frontend, no contract.go)",
		Name:     FixedProjectName,
		Module:   FixedModulePath,
		Services: []string{"task"},
		Entities: map[string][]entityMessage{
			"task": {{
				Name: "Item",
				Fields: []string{
					"string id = 1;",
					"string name = 2;",
					"int64 price_cents = 3;",
					"bool active = 4;",
				},
			}},
		},
	}
}

// projectB differs from projectA in all four derivation inputs at once:
// TWO services (proto set + forge.yaml service list), TWO entities with
// six and four columns spanning int32/double/float/bytes as well
// (migrations + schema), a frontend (forge.yaml features), and an
// internal package (contract.go).
//
// Differing in everything simultaneously is the right trade for a guard
// whose verdict is "did ANYTHING move this file". Per-input attribution
// would need one render per input; the classification only needs to know
// that the file is a function of the inputs at all, and a render costs
// ~20s.
func projectB() fixture {
	return fixture{
		Label:     "B(2 svc, 2 entities, frontend, contract.go)",
		Name:      FixedProjectName,
		Module:    FixedModulePath,
		Services:  []string{"billing", "reporting"},
		Frontends: []string{"web"},
		Entities: map[string][]entityMessage{
			"billing": {{
				Name: "Invoice",
				Fields: []string{
					"string id = 1;",
					"string customer_ref = 2;",
					"int32 line_count = 3;",
					"double total_amount = 4;",
					"bool settled = 5;",
					"bytes payload_blob = 6;",
				},
			}},
			"reporting": {{
				Name: "Snapshot",
				Fields: []string{
					"string id = 1;",
					"string label = 2;",
					"int64 row_count = 3;",
					"float ratio = 4;",
				},
			}},
		},
		InternalPackages: []internalPackage{{Name: "ledger"}},
		GatewayHTTPPort:  18180,
		PublicRPCs:       map[string][]string{"billing": {"GetStatus"}},
		ConfigBlocks: []configBlock{{
			Name: "Ledger",
			Fields: []string{
				`int32 max_per_tick = 1 [(forge.v1.config) = {` +
					`env_var: "LEDGER_MAX_PER_TICK", default_value: "10", ` +
					`description: "Maximum persists per tick"}];`,
			},
		}},
		// A SECOND BINARY with its OWN config. This is what exercises
		// pkg/config/config_gen.go's per-binary half: the template emits a
		// type alias plus Register/Load/ModeOf/Validate per binary-bound
		// message, and a project that annotates nothing gets none of it.
		// Without this the file renders the same AppConfig shim either way
		// and reads as constant — see the note in projectD on why an
		// unexercised input and a genuine constant look identical.
		Binaries: []string{"indexer"},
		BinaryConfigs: []binaryConfigBlock{{
			Binary: "indexer",
			Name:   "Indexer",
			Fields: []string{
				`int32 batch_size = 1 [(forge.v1.config) = {` +
					`env_var: "INDEXER_BATCH_SIZE", flag: "batch-size", ` +
					`default_value: "100", description: "Rows per indexing batch"}];`,
			},
		}},
	}
}

// projectD is the shape the A/B pair cannot express: a project that has
// declared NO entity, and therefore has no migration, plus a service
// whose name shadows a built-in subcommand.
//
// It exists because three Tier-1 files track inputs that both A and B
// happen to hold at the SAME value, which makes each look like a
// constant when it is really only unexercised:
//
//   - db/source_gen.go's body is a two-state projection of "does
//     db/migrations/ hold any .sql yet". A and B both have migrations,
//     so both render the embedded branch. D renders the nil branch.
//   - db/embed_gen.go is emitted ONLY when that answer is yes — so its
//     very presence is the projection, which D is what demonstrates.
//   - cmd/<bin>/cmd/services/register_gen.go carries a NOTE for every
//     service whose kebab name shadows a built-in (server / version /
//     db / help / completion). Neither A nor B names one, so the anchor
//     renders its bare package clause both times. D declares a service
//     named `version`, which is skipped as a subcommand and recorded in
//     the anchor.
//
// It keeps A and B's project identity (see the "held constant" section
// of the package doc): D varies DECLARATIONS only, so a byte difference
// it produces is still attributable to something the user wrote.
func projectD() fixture {
	return fixture{
		Label:  "D(no entities → no migrations, built-in-colliding service name)",
		Name:   FixedProjectName,
		Module: FixedModulePath,
		// `version` collides with the built-in version subcommand. It is a
		// legal service name (ReservedServiceNames covers only the
		// worker/scheduler family), and the cmd-group generator skips its
		// subcommand and records the collision in the anchor.
		Services: []string{"alpha", "version"},
		// No Entities: nothing is marked `// forge:entity`, so `forge
		// scaffold` births no table and db/migrations/ stays empty.
	}
}

// projectC is projectA's inputs under a DIFFERENT project name and
// module path. It is the identity control: A vs C differences are
// attributable to project identity alone, because nothing else moved.
func projectC() fixture {
	fx := projectA()
	fx.Label = "C(= A's inputs, different module path)"
	fx.Name = AltProjectName
	fx.Module = AltModulePath
	return fx
}

// renderResult is one fixture rendered to disk: the producer's Tier-1
// target set plus the bytes of each of those files.
type renderResult struct {
	// Label echoes fixture.Label.
	Label string
	// Root is the project directory the fixture was rendered into.
	Root string
	// Name and Module echo the fixture's project identity, so paths can
	// be normalized when comparing renders with different identities.
	Name   string
	Module string
	// Tier1 is the set of project-relative paths the pipeline TARGETED
	// as Tier-1, read from checksums.Tier1TargetSet — the producer's own
	// bookkeeping, not a scan of the tree.
	Tier1 map[string]bool
	// Bodies maps a Tier-1 path to its marker-stripped content. The
	// marker line is removed because it embeds a hash OF the content:
	// leaving it in would make two byte-identical files compare equal
	// anyway, but stripping keeps the diff output honest about what
	// actually differs.
	Bodies map[string][]byte
	// Missing lists Tier-1 targets that are not on disk after the run
	// (skipped as disowned, or emitted to a path outside the project).
	// Surfaced rather than silently dropped.
	Missing []string
	// ChokepointTargets is the size of checksums.Tier1TargetSet as seen
	// in the process that ran this render, captured immediately after
	// the pipeline finished.
	//
	// Renders happen in a child process (see render_child_test.go), so
	// the rendering process's globals are not the reading process's.
	// This carries the one observation that must be made where the
	// pipeline ran: that the inventory arrived through the checksums
	// chokepoint at all.
	ChokepointTargets int
}

// render scaffolds fx into a fresh directory under parent and runs the
// full pipeline, returning the producer-declared Tier-1 set and the
// resulting bytes.
//
// Every forge invocation happens in-process via cli.Execute so that
// checksums.Tier1TargetSet — package state in THIS process — reflects
// the run. ResetSkipWrite is called immediately before the final
// generate so the captured set belongs to that one run.
func render(parent string, fx fixture) (*renderResult, error) {
	if err := os.MkdirAll(parent, 0o755); err != nil {
		return nil, err
	}

	// --link-forge: the renderer is an unreleased build of this checkout, so
	// a service scaffold must compile forge from it, and that bridge is
	// opt-in.
	args := []string{"project", "new", fx.Name, "--mod", fx.Module, "--link-forge"}
	if len(fx.Services) > 0 {
		args = append(args, "--service", fx.Services[0])
	}
	for _, fe := range fx.Frontends {
		args = append(args, "--frontend", fe)
	}
	if err := forgeIn(parent, args...); err != nil {
		return nil, fmt.Errorf("project new: %w", err)
	}

	root := filepath.Join(parent, fx.Name)

	if err := retargetGatewayListener(root, fx.GatewayHTTPPort); err != nil {
		return nil, err
	}

	for _, svc := range fx.Services[1:] {
		if err := forgeIn(root, "scaffold", "service", svc); err != nil {
			return nil, fmt.Errorf("scaffold service %s: %w", svc, err)
		}
	}

	// Binaries BEFORE their config messages: a message naming a binary
	// with no cmd/<name> is a generate-time error, so the anchor has to
	// exist before the annotation referring to it does.
	for _, bin := range fx.Binaries {
		if err := forgeIn(root, "scaffold", "binary", bin); err != nil {
			return nil, fmt.Errorf("scaffold binary %s: %w", bin, err)
		}
	}

	if err := addConfigBlocks(root, fx.ConfigBlocks); err != nil {
		return nil, err
	}

	if err := addBinaryConfigs(root, fx.BinaryConfigs); err != nil {
		return nil, err
	}

	for _, svc := range fx.Services {
		if err := addPublicRPCs(root, svc, fx.PublicRPCs[svc]); err != nil {
			return nil, err
		}
		if err := appendEntities(root, svc, fx.Entities[svc]); err != nil {
			return nil, err
		}
	}

	// Birth every marked message: migrations + CRUD quintet, then a
	// generate. This is derivation inputs #1 and #2 landing together.
	if len(fx.Entities) > 0 {
		if err := forgeIn(root, "scaffold"); err != nil {
			return nil, fmt.Errorf("scaffold entities: %w", err)
		}
	}

	for _, pkg := range fx.InternalPackages {
		if err := forgeIn(root, "package", "new", pkg.Name); err != nil {
			return nil, fmt.Errorf("package new %s: %w", pkg.Name, err)
		}
	}

	// The measured run. Reset immediately before it so Tier1TargetSet
	// describes THIS generate and not the scaffolds that preceded it.
	checksums.ResetSkipWrite()
	if err := forgeIn(root, "generate"); err != nil {
		return nil, fmt.Errorf("generate: %w", err)
	}

	res := &renderResult{
		Label:  fx.Label,
		Root:   root,
		Name:   fx.Name,
		Module: fx.Module,
		Tier1:  map[string]bool{},
		Bodies: map[string][]byte{},
	}
	for rel := range checksums.Tier1Targets() {
		key := normalizeKey(rel, fx.Name)
		res.Tier1[key] = true
		content, err := os.ReadFile(filepath.Join(root, rel))
		if err != nil {
			res.Missing = append(res.Missing, key)
			continue
		}
		res.Bodies[key] = checksums.StripMarker(content)
	}
	sort.Strings(res.Missing)
	return res, nil
}

// retargetGatewayListener moves the scaffolded `public` Gateway's http listener
// to port in deploy/kcl/ingress.k — the edit a user makes when 18080 is taken.
// A zero port leaves the scaffold's default alone.
func retargetGatewayListener(root string, port int) error {
	if port == 0 {
		return nil
	}
	path := filepath.Join(root, "deploy", "kcl", "ingress.k")
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read %s: %w", path, err)
	}
	const scaffolded = "port = 18080"
	if !strings.Contains(string(raw), scaffolded) {
		return fmt.Errorf("%s no longer declares %q — the fixture's anchor moved with the ingress template", path, scaffolded)
	}
	out := strings.Replace(string(raw), scaffolded, fmt.Sprintf("port = %d", port), 1)
	return atomicfile.WriteFile(path, []byte(out), 0o644)
}

// binPlaceholder stands in for the binary-name path segment so renders
// with different project names produce comparable keys.
const binPlaceholder = "<bin>"

// normalizeKey rewrites the project-name path segments to binPlaceholder,
// so cmd/tgproj/cmd/root.go and cmd/tgother/cmd/root.go are the same
// logical file, and so are proto/tgproj/config/v1/config.proto and
// proto/tgother/config/v1/config.proto (and their gen/ output). Only those
// segments are substituted: the rest of the tree does not embed the
// project name in its paths.
func normalizeKey(rel, projectName string) string {
	rel = filepath.ToSlash(rel)
	if strings.HasPrefix(rel, "cmd/"+projectName+"/") {
		return "cmd/" + binPlaceholder + "/" + strings.TrimPrefix(rel, "cmd/"+projectName+"/")
	}
	slug := configproto.Slug(projectName)
	for _, root := range []string{"proto/", "gen/"} {
		if rest, ok := strings.CutPrefix(rel, root+slug+"/config/"); ok {
			return root + binPlaceholder + "/config/" + rest
		}
	}
	return rel
}

// addConfigBlocks composes each block onto AppConfig in
// the project's config.proto (proto/<project>/config/v1): the `<Name>Config` message is appended at
// file scope and a field referencing it is inserted as the last member of
// AppConfig. This exercises the user-authored config surface that
// pkg/config/config.go and the deploy config projections are derived
// from.
//
// Field tags start at configBlockFirstTag, comfortably above the tags the
// scaffolded AppConfig uses (currently up to 29, with 22 reserved), so an
// injected field never collides with a scaffolded one or with the reserved
// range. A collision would be a proto compile error, not a silent
// mis-render, so this stays a fixture detail rather than a fragile
// coupling.
func addConfigBlocks(root string, blocks []configBlock) error {
	if len(blocks) == 0 {
		return nil
	}
	protoPath := filepath.Join(root, filepath.FromSlash(configproto.ForProject(root).RootPath()))
	raw, err := os.ReadFile(protoPath)
	if err != nil {
		return fmt.Errorf("read config proto: %w", err)
	}
	src := string(raw)

	// AppConfig is the last message in the scaffolded file, so its
	// closing brace is the file's final one.
	closing := strings.LastIndex(src, "}")
	if closing < 0 {
		return fmt.Errorf("config proto has no closing brace")
	}

	var fields, msgs strings.Builder
	for i, blk := range blocks {
		tag := configBlockFirstTag + i
		fmt.Fprintf(&fields, "\n  %sConfig %s = %d;\n",
			blk.Name, strings.ToLower(blk.Name), tag)
		fmt.Fprintf(&msgs, "\nmessage %sConfig {\n", blk.Name)
		for _, f := range blk.Fields {
			msgs.WriteString("  " + f + "\n")
		}
		msgs.WriteString("}\n")
	}

	out := src[:closing] + fields.String() + src[closing:] + msgs.String()
	return atomicfile.WriteFile(protoPath, []byte(out), 0o644)
}

// configBlockFirstTag is the first proto field tag addConfigBlocks
// assigns on AppConfig. Chosen above the scaffolded field tags (and the
// reserved 22) so injected fields never collide.
const configBlockFirstTag = 60

// addBinaryConfigs appends each binary-bound config message to
// the project's config.proto (proto/<project>/config/v1) at FILE SCOPE.
//
// Unlike addConfigBlocks, nothing is composed onto AppConfig: the whole
// point of `(forge.v1.binary_config)` is that the message is the named
// binary's OWN config surface, reached by the binary rather than through
// the project-global root. Composing it onto AppConfig would make it a
// shared block and defeat the input being exercised.
//
// Field tags are the block's own (each message is fresh, so they start
// at 1 and cannot collide with anything scaffolded).
func addBinaryConfigs(root string, blocks []binaryConfigBlock) error {
	if len(blocks) == 0 {
		return nil
	}
	protoPath := filepath.Join(root, filepath.FromSlash(configproto.ForProject(root).RootPath()))
	raw, err := os.ReadFile(protoPath)
	if err != nil {
		return fmt.Errorf("read config proto: %w", err)
	}

	var msgs strings.Builder
	msgs.Write(raw)
	for _, blk := range blocks {
		fmt.Fprintf(&msgs, "\n// %sConfig is the %s binary's own configuration surface.\n",
			blk.Name, blk.Binary)
		fmt.Fprintf(&msgs, "message %sConfig {\n", blk.Name)
		fmt.Fprintf(&msgs, "  option (forge.v1.binary_config) = {binary: %q};\n\n", blk.Binary)
		for _, f := range blk.Fields {
			msgs.WriteString("  " + f + "\n")
		}
		msgs.WriteString("}\n")
	}
	return atomicfile.WriteFile(protoPath, []byte(msgs.String()), 0o644)
}

// addPublicRPCs declares each named RPC on the service with
// `auth_required: false`, plus its request/response messages. This is
// the proto annotation that pkg/middleware/procedures_gen.go projects
// into UnauthenticatedProcedures.
//
// The RPC is inserted inside the `service {}` block, before its closing
// brace; the messages are appended at file scope after it.
func addPublicRPCs(root, svc string, rpcs []string) error {
	if len(rpcs) == 0 {
		return nil
	}
	protoPath := filepath.Join(root, "proto", "services", svc, "v1", svc+".proto")
	raw, err := os.ReadFile(protoPath)
	if err != nil {
		return fmt.Errorf("read %s proto: %w", svc, err)
	}
	src := string(raw)

	closing := strings.LastIndex(src, "}")
	if closing < 0 {
		return fmt.Errorf("%s proto has no closing brace to insert RPCs before", svc)
	}

	var decls, msgs strings.Builder
	for _, rpc := range rpcs {
		fmt.Fprintf(&decls, "\n  // %s is reachable with no credentials.\n"+
			"  rpc %s(%sRequest) returns (%sResponse) {\n"+
			"    option (forge.v1.method) = {auth_required: false};\n  }\n",
			rpc, rpc, rpc, rpc)
		fmt.Fprintf(&msgs, "\nmessage %sRequest {}\n\nmessage %sResponse {\n  string status = 1;\n}\n",
			rpc, rpc)
	}

	out := src[:closing] + decls.String() + src[closing:] + msgs.String()
	return atomicfile.WriteFile(protoPath, []byte(out), 0o644)
}

// appendEntities appends each entity message to the service's proto,
// carrying the `// forge:entity` marker that makes `forge scaffold`
// birth a table for it.
func appendEntities(root, svc string, entities []entityMessage) error {
	if len(entities) == 0 {
		return nil
	}
	protoPath := filepath.Join(root, "proto", "services", svc, "v1", svc+".proto")
	existing, err := os.ReadFile(protoPath)
	if err != nil {
		return fmt.Errorf("read %s proto: %w", svc, err)
	}
	var b strings.Builder
	b.Write(existing)
	for _, e := range entities {
		b.WriteString("\n// forge:entity\nmessage " + e.Name + " {\n")
		for _, f := range e.Fields {
			b.WriteString("  " + f + "\n")
		}
		b.WriteString("}\n")
	}
	return atomicfile.WriteFile(protoPath, []byte(b.String()), 0o644)
}

// forgeIn runs one forge command in-process with dir as the working
// directory. The pipeline resolves the project root from the cwd, and
// cobra reads os.Args, so both are set per call.
func forgeIn(dir string, args ...string) error {
	prev, err := os.Getwd()
	if err != nil {
		return err
	}
	defer func() { _ = os.Chdir(prev) }()
	if err := os.Chdir(dir); err != nil {
		return err
	}
	prevArgs := os.Args
	defer func() { os.Args = prevArgs }()
	os.Args = append([]string{"forge"}, args...)
	return cli.Execute()
}
