package doctor

// payload.go — Connect payload-cap check.
//
// connect-go treats a zero ReadMaxBytes/SendMaxBytes as "no limit"
// ("Setting WithReadMaxBytes to zero allows any message size"), so the
// difference between a 4 MiB cap and an unbounded one is a single
// unassigned struct field. That is a whole class of bug the compiler
// cannot see and no test that only asserts happy-path behaviour will
// catch: the server keeps working, it just also buffers whatever an
// anonymous caller sends it.
//
// The check reads the composition root the project actually compiles —
// cmd/<binary>/cmd/*.go — and asserts that whatever expression feeds
// connect.WithReadMaxBytes / WithSendMaxBytes is provably non-zero at
// that point in the file.
//
// "Provably non-zero" has three shapes, and all three must be accepted or
// the check cries wolf on correct code — which is worse than useless on a
// security gate, because the next FAIL gets ignored too:
//
//   - the field is set in the composite literal,
//   - the field is set by a later selector assignment,
//   - the config value was NORMALIZED in this file before the read. That
//     last one is how forge fixes the ordering hazard at the source:
//     projectServerkitConfig calls skCfg.Normalize() before returning, so
//     the caps are filled in BEFORE anything reads them rather than at
//     serverkit.Run entry, which is too late. A project whose config proto
//     predates the read_max_bytes / send_max_bytes fields has no literal to
//     match on and is still protected entirely by that call.
//
// # HOW "DOES THIS PROJECT SERVE CONNECT HANDLERS?" IS ANSWERED
//
// When no cap appears anywhere the check has to choose between a FAIL that
// names a remotely exploitable hole and a SKIP that says the question does
// not apply. That discriminator used to be a SUBSTRING GREP: any file under
// internal/handlers/ containing the literal text "connect.HandlerOption".
// It was wrong in both directions, and a security check is the worst place
// to be wrong quietly:
//
//   - handlers mounted anywhere else — a composition root that builds the
//     option slice itself, a hand-rolled internal/server/, a generated
//     mount helper that never spells the type — read as "not applicable"
//     and the check reported SKIP on a project that HAD the exposure;
//   - a file merely MENTIONING the type in a doc comment read as proof and
//     the check reported FAIL on a project that did not.
//
// The determination now goes to the artefacts that DECLARE the answer,
// which is the same discipline deploy_config_drift.go follows (cross-check
// the render against config.LoadProjectDir; never read source text):
//
//  1. The composition root's own AST — the cmd/** parse this check already
//     performs. A reference to the connect-go runtime type HandlerOption,
//     or a call to a generated <pkg>connect.New*Handler constructor, is
//     direct proof, resolved through the file's IMPORTS so a local
//     identifier that merely happens to be spelled `connect` cannot fake it
//     and a comment cannot produce it at all.
//  2. The proto descriptor (gen/forge_descriptor.json, read through
//     codegen.ParseServicesFromProtos — "the single authoritative,
//     non-brittle source" per IntrospectComponents). A declared Connect
//     service IS a mounted one: forge generates the mount for every service
//     in the descriptor and serverkit.RequireMounted refuses to boot a
//     binary that left one unmounted. Zero services in a readable
//     descriptor is an equally authoritative "no".
//  3. config.LoadProjectDir's derived Kind, which comes from the project's
//     real sources (deploy/kcl, pkg/app, internal/handlers, proto/services,
//     cmd/<name>/main.go). A cli or library project serves no Connect
//     handlers.
//
// Anything those three cannot settle is UNDETERMINED, never Skip and never
// Fail — see the StatusSkip vs StatusUnknown note in doctor.go. The two
// shapes that land there are named in [mountingVerdict]'s call sites: a
// descriptor that will not parse (or is stale against the protos on disk),
// and a service-shaped project whose descriptor has not been generated yet.
// Both are literally "forge could not obtain the facts", and answering them
// with a confident Skip is the failure this rewrite exists to remove.

import (
	"context"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	pathpkg "path"
	"path/filepath"
	"sort"
	"strconv"
	"strings"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
)

// payloadCapOptions are the connect handler options whose argument must
// be non-zero for a cap to exist.
var payloadCapOptions = map[string]string{
	"WithReadMaxBytes": "inbound request",
	"WithSendMaxBytes": "outbound response",
}

// capUse is one observed call site.
type capUse struct {
	file   string // path relative to the project dir
	line   int
	option string // WithReadMaxBytes / WithSendMaxBytes
	arg    string // rendered argument expression
	// field is the selector's field name when arg is `x.Field`, else "".
	field string
	// recv is the selector's receiver name when arg is `x.Field`, else "".
	recv string
}

// CheckPayloadLimits verifies the generated server caps Connect payload
// sizes.
//
// A missing cap is remotely exploitable without credentials: the server
// buffers the entire request body before any validation runs, so one
// request sized at the pod's memory limit is an OOMKill, and the
// generated deploy manifests ship a memory limit far below what a
// single large request costs.
//
// The check fails on three shapes:
//   - the option is passed a literal 0
//   - the option is passed `cfg.Field` where neither an assignment to that
//     field nor a `cfg.Normalize()` call appears in the same file (the
//     value reaching connect is the zero value, whatever a defaults helper
//     does later downstream)
//   - the project mounts Connect handlers and passes no cap at all
//
// That last shape reports UNDETERMINED rather than either verdict when
// forge cannot establish whether Connect handlers are served at all; see
// the header note.
func CheckPayloadLimits(_ context.Context, env *Environment) CheckResult {
	cmdDir := filepath.Join(env.ProjectDir, "cmd")
	if _, err := os.Stat(cmdDir); err != nil {
		return CheckResult{Status: StatusSkip, Message: "no cmd/ tree — nothing to inspect"}
	}

	var (
		uses     []capUse
		problems []string
		mounting []string
		files    int
	)

	walkErr := filepath.WalkDir(cmdDir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			// An unreadable SUBTREE is not this check's failure — the
			// composition root is still readable and still answers.
			// An unreadable ROOT is a different animal: it means nothing
			// was read at all, and swallowing it left the check reporting
			// "no Go files under cmd/" (a Skip — "not applicable") about a
			// tree it never opened. Propagate it so the caller can say it
			// could not obtain the facts.
			if path == cmdDir {
				return err
			}
			return nil //nolint:nilerr // an unreadable subtree is not this check's failure
		}
		if d.IsDir() || !strings.HasSuffix(path, ".go") || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		files++
		fset := token.NewFileSet()
		file, perr := parser.ParseFile(fset, path, nil, parser.SkipObjectResolution)
		if perr != nil {
			return nil
		}
		rel, _ := filepath.Rel(env.ProjectDir, path)
		assigned := assignedFields(file)
		normalized := normalizedReceivers(file)
		mounting = append(mounting, connectHandlerEvidence(file, fset, rel)...)
		chainUses, chainProblems := chainCapUses(file, fset, rel)
		uses = append(uses, chainUses...)
		problems = append(problems, chainProblems...)

		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 1 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			if _, tracked := payloadCapOptions[sel.Sel.Name]; !tracked {
				return true
			}
			use := capUse{
				file:   rel,
				line:   fset.Position(call.Pos()).Line,
				option: sel.Sel.Name,
				arg:    exprString(call.Args[0]),
			}
			if argSel, isSel := call.Args[0].(*ast.SelectorExpr); isSel {
				use.field = argSel.Sel.Name
				if recv, isIdent := argSel.X.(*ast.Ident); isIdent {
					use.recv = recv.Name
				}
			}
			uses = append(uses, use)

			switch {
			case use.arg == "0":
				problems = append(problems, fmt.Sprintf(
					"%s:%d: %s(0) — connect-go reads a zero cap as UNLIMITED %s payloads",
					use.file, use.line, use.option, payloadCapOptions[use.option]))
			case use.field != "" && !assigned[use.field] && !normalized[use.recv]:
				problems = append(problems, fmt.Sprintf(
					"%s:%d: %s(%s) but %s is never assigned in this file and %s is never normalized — "+
						"it reaches connect as 0, which means UNLIMITED %s payloads (a defaults helper "+
						"that runs later, e.g. inside serverkit.Run, cannot retroactively change the "+
						"value already captured here; call %s.Normalize() before this read)",
					use.file, use.line, use.option, use.arg, use.field, use.recv,
					payloadCapOptions[use.option], use.recv))
			}
			return true
		})
		return nil
	})
	if walkErr != nil {
		// The composition root is the only place the answer lives, so an
		// unreadable cmd/ is not a benign "nothing found" — forge could not
		// obtain the facts. UNDETERMINED, not a warning that reads like a
		// finished answer (see the StatusSkip vs StatusUnknown note in
		// doctor.go).
		return CheckResult{
			Status:   StatusUnknown,
			Message:  "could not walk cmd/ — the composition root could not be read",
			Evidence: walkErr.Error(),
		}
	}
	if files == 0 {
		return CheckResult{Status: StatusSkip, Message: "no Go files under cmd/"}
	}

	if len(uses) == 0 {
		// No caps at all is only a finding when the project serves
		// Connect handlers; a CLI-shaped cmd tree legitimately has none.
		verdict := determineConnectMounting(env.ProjectDir, mounting)
		if verdict.state == mountingNone {
			return CheckResult{
				Status:   StatusSkip,
				Message:  "no Connect handlers mounted — payload caps not applicable",
				Evidence: verdict.reason,
			}
		}
		if verdict.state == mountingMounted {
			return CheckResult{
				Status: StatusFail,
				Message: "Connect handlers are mounted with no WithReadMaxBytes/WithSendMaxBytes — " +
					"request and response payloads are unbounded",
				Evidence: verdict.reason,
			}
		}
		return CheckResult{
			Status: StatusUnknown,
			Message: "no WithReadMaxBytes/WithSendMaxBytes anywhere under cmd/, and forge could not " +
				"establish whether this project serves Connect handlers",
			Evidence: verdict.reason,
		}
	}

	if len(problems) > 0 {
		sort.Strings(problems)
		return CheckResult{
			Status:   StatusFail,
			Message:  fmt.Sprintf("%d Connect payload cap(s) resolve to 0 (unlimited)", len(problems)),
			Evidence: strings.Join(problems, "\n"),
		}
	}

	set := make([]string, 0, len(uses))
	for _, u := range uses {
		set = append(set, fmt.Sprintf("%s:%d %s(%s)", u.file, u.line, u.option, u.arg))
	}
	sort.Strings(set)
	return CheckResult{
		Status:   StatusPass,
		Message:  fmt.Sprintf("%d Connect payload cap(s) set to a non-zero value", len(uses)),
		Evidence: strings.Join(set, "\n"),
	}
}

// serverkitPath is the import path of forge's serverkit, whose NewChain
// builds the handler options — caps included — from a ChainDeps.
const serverkitPath = "github.com/reliant-labs/forge/pkg/serverkit"

// chainCapUses reports the payload caps of every serverkit.NewChain call in
// file. NewChain applies the caps itself and maps a zero (an unset field, or a
// config value that reads zero) to its 4 MiB default, so a chain is capped
// unless a cap is a NEGATIVE literal — NewChain's spelling of "no cap".
func chainCapUses(file *ast.File, fset *token.FileSet, rel string) (uses []capUse, problems []string) {
	name := ""
	for _, imp := range file.Imports {
		if p, err := strconv.Unquote(imp.Path.Value); err == nil && p == serverkitPath {
			name = pathpkg.Base(p)
			if imp.Name != nil {
				name = imp.Name.Name
			}
		}
	}
	if name == "" || name == "_" {
		return nil, nil
	}
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok || len(call.Args) != 1 {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "NewChain" {
			return true
		}
		if pkg, ok := sel.X.(*ast.Ident); !ok || pkg.Name != name {
			return true
		}
		fields := map[string]ast.Expr{}
		if lit, ok := call.Args[0].(*ast.CompositeLit); ok {
			for _, elt := range lit.Elts {
				if kv, ok := elt.(*ast.KeyValueExpr); ok {
					if key, ok := kv.Key.(*ast.Ident); ok {
						fields[key.Name] = kv.Value
					}
				}
			}
		}
		line := fset.Position(call.Pos()).Line
		for _, f := range []struct{ field, kind string }{
			{"ReadMaxBytes", "inbound request"},
			{"SendMaxBytes", "outbound response"},
		} {
			use := capUse{file: rel, line: line, option: "NewChain." + f.field, arg: "default 4 MiB"}
			if v, set := fields[f.field]; set {
				use.arg = exprString(v)
				if u, neg := v.(*ast.UnaryExpr); neg && u.Op == token.SUB {
					problems = append(problems, fmt.Sprintf(
						"%s:%d: serverkit.ChainDeps{%s: %s} — a negative cap is UNLIMITED %s payloads",
						rel, line, f.field, use.arg, f.kind))
				}
			}
			uses = append(uses, use)
		}
		return true
	})
	return uses, problems
}

// assignedFields collects every struct-field name the file assigns —
// both as a composite-literal key (`Config{ReadMaxBytes: n}`) and as a
// selector assignment (`cfg.ReadMaxBytes = n`).
func assignedFields(file *ast.File) map[string]bool {
	assigned := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		switch node := n.(type) {
		case *ast.KeyValueExpr:
			if key, ok := node.Key.(*ast.Ident); ok {
				assigned[key.Name] = true
			}
		case *ast.AssignStmt:
			for _, lhs := range node.Lhs {
				if sel, ok := lhs.(*ast.SelectorExpr); ok {
					assigned[sel.Sel.Name] = true
				}
			}
		}
		return true
	})
	return assigned
}

// normalizedReceivers collects every identifier the file calls
// `.Normalize()` on. A config normalized in this file has had its unset
// caps filled in BEFORE any read here, which is exactly the guarantee this
// check is looking for — and the only guarantee available to a project
// whose config proto does not declare the cap fields.
func normalizedReceivers(file *ast.File) map[string]bool {
	normalized := map[string]bool{}
	ast.Inspect(file, func(n ast.Node) bool {
		call, ok := n.(*ast.CallExpr)
		if !ok {
			return true
		}
		sel, ok := call.Fun.(*ast.SelectorExpr)
		if !ok || sel.Sel.Name != "Normalize" {
			return true
		}
		if recv, isIdent := sel.X.(*ast.Ident); isIdent {
			normalized[recv.Name] = true
		}
		return true
	})
	return normalized
}

// connectMounting is the answer to the one question that separates this
// check's FAIL from its SKIP: does the project serve Connect handlers?
type connectMounting int

const (
	// mountingUndetermined — forge could not obtain the facts. NOT a
	// pass and NOT "not applicable"; the caller reports StatusUnknown.
	mountingUndetermined connectMounting = iota
	// mountingNone — an authoritative artefact says this project serves
	// no Connect handlers, so payload caps genuinely do not apply.
	mountingNone
	// mountingMounted — an authoritative artefact says it does.
	mountingMounted
)

// mountingVerdict pairs the answer with the artefact it came from, so the
// evidence line names WHY forge believes what it reports — including for
// the undetermined case, where naming the missing fact is the whole value.
type mountingVerdict struct {
	state  connectMounting
	reason string
}

// descriptorFile is the generated proto descriptor's project-relative
// path. Read directly (rather than through codegen) only to tell an ABSENT
// descriptor from an unreadable one: codegen.ParseServicesFromProtos
// collapses both into (nil, nil), and that difference is exactly the
// difference between Skip and Unknown here. It also keeps the "run forge
// generate" info line codegen prints for a missing descriptor out of
// doctor's output.
var descriptorFile = filepath.Join("gen", "forge_descriptor.json")

// determineConnectMounting answers whether the project serves Connect
// handlers, from the artefacts that DECLARE the answer. See the file
// header for why each source is authoritative and why anything they cannot
// settle is undetermined rather than skipped.
//
// cmdEvidence is the direct proof gathered from the cmd/** parse the
// caller already performed (see connectHandlerEvidence).
func determineConnectMounting(projectDir string, cmdEvidence []string) mountingVerdict {
	// 1. Direct proof from the composition root. Nothing downstream can
	// overturn code that references connect-go's handler surface.
	if len(cmdEvidence) > 0 {
		return mountingVerdict{
			state:  mountingMounted,
			reason: "Connect handler wiring in the composition root:\n" + strings.Join(cmdEvidence, "\n"),
		}
	}

	// 2. The proto descriptor. A declared Connect service IS a mounted one
	// — forge generates the mount for every service in the descriptor and
	// serverkit.RequireMounted refuses to boot a binary that left one
	// unmounted — so the descriptor answers both directions.
	descPath := filepath.Join(projectDir, descriptorFile)
	if _, statErr := os.Stat(descPath); statErr == nil {
		defs, err := codegen.ParseServicesFromProtos("", projectDir)
		if err != nil {
			return mountingVerdict{
				state: mountingUndetermined,
				reason: fmt.Sprintf("%s could not be read, so the project's Connect services "+
					"cannot be enumerated: %v", descriptorFile, err),
			}
		}
		if len(defs) > 0 {
			names := make([]string, 0, len(defs))
			for _, d := range defs {
				names = append(names, d.Name)
			}
			sort.Strings(names)
			return mountingVerdict{
				state: mountingMounted,
				reason: fmt.Sprintf("%s declares %d Connect service(s): %s",
					descriptorFile, len(names), strings.Join(names, ", ")),
			}
		}
		return mountingVerdict{
			state:  mountingNone,
			reason: fmt.Sprintf("%s declares no Connect services", descriptorFile),
		}
	}

	// 3. No descriptor. The project's derived kind still settles the
	// cli/library shapes, for which the descriptor is legitimately absent
	// forever — the same reasoning codegen's noticeMissingDescriptor uses
	// before it declines to nag about a file that will never exist.
	cfg, err := config.LoadProjectDir(projectDir)
	if err != nil {
		return mountingVerdict{
			state: mountingUndetermined,
			reason: fmt.Sprintf("no %s and no readable forge.yaml, so neither the project's Connect "+
				"services nor its kind could be established: %v", descriptorFile, err),
		}
	}
	if kind := cfg.EffectiveKind(); kind != config.ProjectKindService {
		return mountingVerdict{
			state: mountingNone,
			reason: fmt.Sprintf("forge.yaml resolves to a %s project (kind derived from the project's "+
				"own sources), which serves no Connect handlers", kind),
		}
	}
	return mountingVerdict{
		state: mountingUndetermined,
		reason: fmt.Sprintf("service-shaped project with no %s — the descriptor is the authoritative "+
			"list of Connect services and it has not been generated (run `forge generate`), so "+
			"forge cannot tell whether any handler is mounted", descriptorFile),
	}
}

// connectHandlerEvidence returns the Connect handler wiring one parsed file
// declares, as `file:line — what` lines.
//
// Two shapes count, and both are AST facts resolved through the file's
// IMPORTS rather than text matches:
//
//   - a reference to the connect-go runtime's HandlerOption type. It is a
//     SERVER-side type (the client half is ClientOption), so naming it is
//     proof the file wires handlers.
//   - a call to a generated Connect package's New…Handler constructor —
//     protoc-gen-connect-go emits those into a package whose import path
//     ends in "connect", and calling one IS the mount.
//
// Import resolution is what makes this immune to the two failures of the
// grep it replaces: a comment produces no AST node at all, and a local
// variable that merely happens to be named `connect` is not bound to any
// import so it never matches.
func connectHandlerEvidence(file *ast.File, fset *token.FileSet, rel string) []string {
	runtime, generated := connectImportNames(file)
	if len(runtime) == 0 && len(generated) == 0 {
		return nil
	}

	var out []string
	seen := map[string]bool{}
	add := func(pos token.Pos, what string) {
		line := fmt.Sprintf("%s:%d — %s", rel, fset.Position(pos).Line, what)
		if seen[line] {
			return
		}
		seen[line] = true
		out = append(out, line)
	}

	ast.Inspect(file, func(n ast.Node) bool {
		sel, ok := n.(*ast.SelectorExpr)
		if !ok {
			return true
		}
		pkg, ok := sel.X.(*ast.Ident)
		if !ok {
			return true
		}
		switch {
		case runtime[pkg.Name] && sel.Sel.Name == "HandlerOption":
			add(sel.Pos(), fmt.Sprintf("references %s.HandlerOption (connect-go's server-side option type)", pkg.Name))
		case generated[pkg.Name] && isConnectHandlerConstructor(sel.Sel.Name):
			add(sel.Pos(), fmt.Sprintf("calls %s.%s (a generated Connect handler constructor)", pkg.Name, sel.Sel.Name))
		}
		return true
	})
	return out
}

// isConnectHandlerConstructor matches protoc-gen-connect-go's handler
// constructor naming — New<Service>Handler. The bare names "NewHandler" and
// "NewServiceHandler" are deliberately included; anything shorter is not a
// constructor.
func isConnectHandlerConstructor(name string) bool {
	return strings.HasPrefix(name, "New") && strings.HasSuffix(name, "Handler") &&
		len(name) >= len("NewHandler")
}

// connectRuntimePaths are the import paths of the connect-go runtime — the
// current module and its pre-rename spelling. Matched in FULL rather than
// by last path element so a project's own internal/connect package cannot
// be mistaken for the runtime whose HandlerOption type this check treats as
// proof.
var connectRuntimePaths = map[string]bool{
	"connectrpc.com/connect":         true,
	"github.com/bufbuild/connect-go": true,
}

// connectImportNames resolves the FILE-LOCAL identifiers bound to the
// connect-go runtime and to generated Connect packages, so the evidence
// scan reads what the code imports rather than what it happens to spell.
//
// Generated packages are the `<proto package>connect` leaves
// protoc-gen-connect-go emits (billingv1connect, …) — identified by the
// suffix because the prefix is the project's own proto package name.
func connectImportNames(file *ast.File) (runtime, generated map[string]bool) {
	runtime = map[string]bool{}
	generated = map[string]bool{}
	for _, spec := range file.Imports {
		importPath, err := strconv.Unquote(spec.Path.Value)
		if err != nil {
			continue
		}
		base := pathpkg.Base(importPath)
		isRuntime := connectRuntimePaths[importPath]
		isGenerated := !isRuntime && strings.HasSuffix(base, "connect")
		if !isRuntime && !isGenerated {
			continue
		}
		local := base
		if isRuntime {
			// connect-go's package name is `connect` whatever the path's
			// last element is; an explicit alias below overrides it.
			local = "connect"
		}
		if spec.Name != nil {
			if spec.Name.Name == "_" || spec.Name.Name == "." {
				// A blank import wires nothing, and a dot import puts the
				// names in scope unqualified where this selector scan
				// cannot see them — neither is evidence either way.
				continue
			}
			local = spec.Name.Name
		}
		if isRuntime {
			runtime[local] = true
		} else {
			generated[local] = true
		}
	}
	return runtime, generated
}

// exprString renders the small subset of expressions these options are
// called with, so the evidence quotes the source rather than an AST
// dump.
func exprString(e ast.Expr) string {
	switch node := e.(type) {
	case *ast.Ident:
		return node.Name
	case *ast.BasicLit:
		return node.Value
	case *ast.SelectorExpr:
		return exprString(node.X) + "." + node.Sel.Name
	case *ast.CallExpr:
		return exprString(node.Fun) + "(…)"
	case *ast.BinaryExpr:
		return exprString(node.X) + " " + node.Op.String() + " " + exprString(node.Y)
	case *ast.ParenExpr:
		return "(" + exprString(node.X) + ")"
	case *ast.UnaryExpr:
		return node.Op.String() + exprString(node.X)
	default:
		return strconv.Quote(fmt.Sprintf("%T", e))
	}
}
