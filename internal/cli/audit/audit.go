// Package audit holds the `forge project audit` command group — a comprehensive
// snapshot of project state designed to orient an LLM (or human) without
// forcing them to grep ten different directories. It rolls up:
//
//   - Forge version pin (forge.yaml forge_version vs binary buildinfo).
//   - Project shape (kind, services + RPC counts, workers, operators,
//     frontends).
//   - Convention compliance (rolled up forge lint counts per category).
//   - Codegen state (certified-file census via the embedded forge:hash markers,
//     orphan _gen files, uncommitted user edits to forge-space files).
//   - Proto-vs-migration alignment (entity tables vs db/migrations/).
//   - Migration safety summary (allowed_destructive count, latest
//     migration timestamp, destructive_change severity).
//   - FORGE_SCAFFOLD marker counts (P0 sharpening surface).
//   - Deps health (go.sum freshness vs go.mod, gen/ presence).
//
// JSON output groups checks by category with status: ok|warn|error so a
// sub-agent can branch on `.codegen.status == "warn"` directly.
//
// It is a dir-nested command group (the devspace idiom). The few categories
// it cannot compute without package-cli internals — the KCL-entity-typed
// ingress / external-builds categories, plus the
// service-registry / env-discovery / drift helpers — are reached through
// factory.AuditAPI (function values internal/cli registers via SetAuditAPI).
// The group never imports internal/cli, so the registry indirection in the
// factory package keeps the dependency one-directional (group → factory) and
// the import cycle broken. init() self-registers the command with the
// factory.
package audit

import (
	"encoding/json"
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/buildinfo"
	"github.com/reliant-labs/forge/internal/checksums"
	"github.com/reliant-labs/forge/internal/cli/audittype"
	"github.com/reliant-labs/forge/internal/cli/cmdutil"
	"github.com/reliant-labs/forge/internal/cli/factory"
	"github.com/reliant-labs/forge/internal/cli/lint"
	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/config"
	"github.com/reliant-labs/forge/internal/forgecompat"
	"github.com/reliant-labs/forge/internal/generator"
	"github.com/reliant-labs/forge/internal/linter/forgeconv"
	"github.com/reliant-labs/forge/internal/linter/migrationlint"
)

func init() { factory.Register(newCmd) }

// dirExists is the trivial os.Stat wrapper the audit categories use for
// directory-presence gates. Per cmdutil's policy, this one-line stdlib
// check is duplicated locally rather than shared.
func dirExists(path string) bool {
	info, err := os.Stat(path)
	if err != nil {
		return false
	}
	return info.IsDir()
}

// Report is the top-level JSON structure emitted by `forge project audit --json`.
// Field order is stable so diffing two audits is human-readable.
type Report struct {
	ProjectName   string                        `json:"project_name"`
	ProjectKind   string                        `json:"project_kind"`
	BinaryVersion string                        `json:"binary_version"`
	GeneratedAt   time.Time                     `json:"generated_at"`
	Categories    map[string]audittype.Category `json:"categories"`
	OverallStatus audittype.Status              `json:"overall_status"`
}

// auditCategoryOrder pins the print-order so human output stays stable
// regardless of map iteration. Categories not in this list fall back to
// alphabetical at the end.
var auditCategoryOrder = []string{
	"version",
	"shape",
	"features",
	"ingress",
	"environments",
	"external_builds",
	"prerequisites",
	"conventions",
	"codegen",
	"migration_safety",
	"optional_deps_guard",
	"config_deps",
	"scaffold_markers",
	"crud_stubs",
	"unscoped_auth",
	"file_sizes",
	"orphan_stubs",
	"unhandled_rpcs",
	"deps",
}

// CategoryNames returns every key `forge project audit --json` can put under
// `.categories`, in print order. It is the authoritative category set:
// buildAuditReport assigns exactly these keys, and auditCategoryOrder above
// pins their order. Exported so docs about the JSON shape (the `audit-json`
// skill) can be fact-checked against the code that emits it rather than
// against a hand-copied list that silently rots.
func CategoryNames() []string {
	return append([]string(nil), auditCategoryOrder...)
}

func newCmd(f *factory.Factory) *cobra.Command {
	var jsonOut bool
	cmd := &cobra.Command{
		Use:   "audit",
		Short: "Print a comprehensive project state snapshot",
		Long: `Print a comprehensive snapshot of forge project state.

Audit reports forge version pin, project shape, lint roll-ups, codegen
state, proto vs migration alignment, scaffold markers, and dep health.
Use --json for machine-readable output (sub-agents).

EXIT CODE. Audit exits non-zero when a category reports an ERROR (✗), and
zero when the worst finding is a warning (⚠). Warnings are reported and
never gate: a freshly-scaffolded project legitimately carries several, so
failing on them would make forge's own output fail forge's own gate.

Categories that can error are ones you armed. unscoped_auth is the
clearest case: it warns about authenticated RPCs that never resolve the
caller, and becomes an error only for RPCs over a table whose migration
declares a forge:owner column — your sentence, in your schema, is what
turns the advice into a gate.

Examples:
  forge project audit            # human-readable; exits 1 on any ✗
  forge project audit --json     # machine-readable (same exit code)`,
		RunE: func(cmd *cobra.Command, args []string) error {
			return runAudit(f, jsonOut)
		},
	}
	cmd.Flags().BoolVar(&jsonOut, "json", false, "Output as JSON")
	return cmd
}

func runAudit(f *factory.Factory, jsonOut bool) error {
	report, err := buildAuditReport(f, ".")
	if err != nil {
		return err
	}
	if jsonOut {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(report); err != nil {
			return err
		}
		// The gate applies to --json too: a machine reading the report
		// through a shell still reads $?, and a JSON mode that always
		// exited 0 would be the same silence one layer down.
		return gateOnReport(report)
	}
	printAuditReport(os.Stdout, report)
	return gateOnReport(report)
}

// buildAuditReport collects every category's data and rolls up the
// overall status. Errors in individual category collectors are folded
// into a "warn" status for that category — we never bail the whole audit
// because a single grep failed; partial information beats nothing.
func buildAuditReport(f *factory.Factory, projectDir string) (*Report, error) {
	abs, err := filepath.Abs(projectDir)
	if err != nil {
		return nil, fmt.Errorf("resolve project dir: %w", err)
	}

	store, cfgErr := f.Audit.LoadProjectStoreFrom(filepath.Join(abs, "forge.yaml"))
	if cfgErr != nil && !errors.Is(cfgErr, cmdutil.ErrProjectConfigNotFound) {
		return nil, fmt.Errorf("load project config: %w", cfgErr)
	}
	var cfg *config.ProjectConfig
	if store != nil {
		cfg = store.Config()
	}

	report := &Report{
		BinaryVersion: buildinfo.Version(),
		GeneratedAt:   time.Now().UTC(),
		Categories:    make(map[string]audittype.Category),
	}
	if cfg != nil {
		report.ProjectName = cfg.Name
		report.ProjectKind = cfg.EffectiveKind()
	} else {
		report.ProjectName = filepath.Base(abs)
		report.ProjectKind = "unknown"
	}

	report.Categories["version"] = auditVersion(cfg, abs)
	report.Categories["shape"] = auditShape(f, cfg, abs)
	report.Categories["features"] = auditFeatures(cfg)
	if cfg != nil && cfg.Features.IngressEnabled() {
		report.Categories["ingress"] = f.Audit.Ingress(cfg, abs)
	}
	report.Categories["environments"] = auditEnvironments(f, abs)
	report.Categories["external_builds"] = f.Audit.ExternalBuilds(cfg, abs)
	if f.Audit.Prerequisites != nil {
		report.Categories["prerequisites"] = f.Audit.Prerequisites(cfg, abs)
	}
	report.Categories["conventions"] = auditConventions(cfg, abs)
	report.Categories["codegen"] = auditCodegen(f, cfg, abs)
	report.Categories["migration_safety"] = auditMigrationSafety(cfg, abs)
	report.Categories["optional_deps_guard"] = auditOptionalDepsGuard(abs)
	report.Categories["config_deps"] = auditConfigDeps(abs)
	report.Categories["scaffold_markers"] = auditScaffoldMarkers(abs)
	report.Categories["crud_stubs"] = auditCRUDStubs(abs)
	report.Categories["unscoped_auth"] = auditUnscopedAuth(cfg, abs)
	report.Categories["file_sizes"] = auditFileSizes(abs)
	report.Categories["orphan_stubs"] = auditOrphanStubs(f, cfg, abs)
	report.Categories["unhandled_rpcs"] = auditUnhandledRPCs(f, abs)
	report.Categories["deps"] = auditDeps(abs)

	report.OverallStatus = rollupStatus(report.Categories)
	return report, nil
}

// rollupStatus collapses per-category statuses into one overall verdict.
// "error" beats "warn" beats "ok", same precedence forge doctor uses.
func rollupStatus(cats map[string]audittype.Category) audittype.Status {
	worst := audittype.StatusOK
	for _, c := range cats {
		switch c.Status {
		case audittype.StatusError:
			return audittype.StatusError
		case audittype.StatusWarn:
			worst = audittype.StatusWarn
		}
	}
	return worst
}

// ciInstallRefRE extracts the ref a generated CI workflow pins forge to,
// from a `go install github.com/reliant-labs/forge/cmd/forge@<ref>` line.
var ciInstallRefRE = regexp.MustCompile(
	`go install "?github\.com/reliant-labs/forge/cmd/forge@(\S+)`)

// ciForgePin reads the forge install ref pinned by the generated CI
// workflow (.github/workflows/ci.yml). Returns "" when there is no
// workflow or no pin line — a project may legitimately have neither.
//
// A workflow scaffolded by a current forge pins NOTHING: it installs the
// version go.mod resolves at run time (`…/cmd/forge@${v}`), so it cannot
// diverge from the project and is not a pin at all. Only a LITERAL ref — an
// older scaffold's, or a hand-written one — is reported.
func ciForgePin(projectDir string) string {
	data, err := os.ReadFile(filepath.Join(projectDir, ".github", "workflows", "ci.yml"))
	if err != nil {
		return ""
	}
	if m := ciInstallRefRE.FindSubmatch(data); m != nil {
		ref := strings.Trim(strings.TrimSpace(string(m[1])), `"'`)
		if strings.Contains(ref, "$") {
			return ""
		}
		return ref
	}
	return ""
}

// pinIdentity reduces a forge version pin to the thing it actually names:
// a COMMIT, when one can be recovered, otherwise the pin verbatim.
//
// forge writes the same commit two ways, by design:
//
//	forge.yaml  forge_version: v0.0.4-0.20260726192353-613340e38be3+dirty
//	ci.yml      go install …/cmd/forge@613340e38be372ec4014c96e714653d712073fa7
//
// The first is the binary's module version; the second has to be a ref
// `go install` can resolve, so a dev build falls back to the git SHA. Both
// designate commit 613340e38be3. A released binary writes v1.2.3 in both
// places and lands here unchanged.
//
// Recovered forms:
//   - a bare hex ref (>= 7 chars) → its first 12 chars,
//   - a Go pseudo-version's trailing 12-hex revision, with any build
//     metadata (`+dirty`) stripped first.
//
// Anything else (a semver tag, a branch name) is returned trimmed, so two
// genuinely different pins still compare unequal.
func pinIdentity(v string) string {
	v = strings.TrimSpace(v)
	if v == "" {
		return ""
	}
	// Strip semver build metadata: `+dirty` says the tree was modified, not
	// which commit it was modified from.
	base := v
	if i := strings.IndexByte(base, '+'); i >= 0 {
		base = base[:i]
	}
	if isHexRef(base) && len(base) >= 7 {
		return strings.ToLower(base[:12])
	}
	// Go pseudo-version: vX.Y.Z-<pre.>0.<timestamp>-<12 hex>
	if i := strings.LastIndexByte(base, '-'); i >= 0 {
		if rev := base[i+1:]; len(rev) == 12 && isHexRef(rev) {
			return strings.ToLower(rev)
		}
	}
	return v
}

// isHexRef reports whether s is all hex digits — the shape of a git object
// name.
func isHexRef(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		switch {
		case r >= '0' && r <= '9', r >= 'a' && r <= 'f', r >= 'A' && r <= 'F':
		default:
			return false
		}
	}
	return true
}

// auditVersion compares the project's forge_version pins against the
// running binary AND against each other. It performs a REAL string
// comparison: it does not borrow the once-per-session nudge silencing of
// forgeVersionMismatchWarning (which intentionally goes quiet on dev /
// pseudo-version binaries) — `forge project audit` is an explicit diagnostic, so
// a stale or divergent pin must be reported, never papered over with a
// false "matches binary" (fr-82b717f521).
//
// Mismatches surface as warnings (not errors): running a newer binary is
// usually fine and `forge project upgrade` realigns. The category also flags when
// the three independent pins (forge.yaml / CI workflow / .forge state)
// disagree with each other, since nothing else does.
func auditVersion(cfg *config.ProjectConfig, projectDir string) audittype.Category {
	binv := buildinfo.Version()
	if cfg == nil {
		return audittype.Category{
			Status:  audittype.StatusError,
			Summary: "no forge.yaml found — not a forge project",
			Details: map[string]any{"binary_version": binv},
		}
	}
	pinned := cfg.EffectiveForgeVersion()
	details := map[string]any{
		"pinned_version": pinned,
		"binary_version": binv,
	}

	// Collect the other independent pins so divergence can be surfaced.
	ciPin := ciForgePin(projectDir)
	if ciPin != "" {
		details["ci_pin"] = ciPin
	}
	var statePin string
	if cs, err := generator.LoadChecksums(projectDir); err == nil && cs != nil {
		statePin = strings.TrimSpace(cs.ForgeVersion)
		if statePin != "" {
			details["state_pin"] = statePin
		}
	}

	var summaries []string
	status := audittype.StatusOK

	// 1. forge.yaml pin vs running binary — a REAL compare. We do not
	//    silence on dev/pseudo binaries here.
	switch {
	case strings.TrimSpace(cfg.ForgeVersion) == "":
		status = audittype.StatusWarn
		summaries = append(summaries,
			fmt.Sprintf("no forge_version declared in forge.yaml (binary is %s) — run `%s project upgrade` to set a baseline", binv, cmdutil.Name()))
		details["hint"] = fmt.Sprintf("run `%s project upgrade` to set + align the pin", cmdutil.Name())
	case strings.TrimSpace(cfg.ForgeVersion) == "0.0.0":
		// 0.0.0 is the DELIBERATE sentinel for forge-as-its-own-first-user
		// (see the comment in forge.yaml): the repo builds itself and pins
		// 0.0.0 so `forge project upgrade` always surfaces the migration skills. It
		// is intentionally never equal to the pseudo-version binary, so a
		// "does NOT match binary" warning here would be pure noise. Treat
		// the explicit 0.0.0 pin as ✓ / informational, not a stale pin.
		details["intentional"] = "0.0.0 is the deliberate forge-as-its-own-first-user sentinel"
		summaries = append(summaries,
			fmt.Sprintf("forge_version pinned to 0.0.0 (deliberate first-user sentinel; binary is %s) — not a stale pin", binv))
	case pinned == binv:
		summaries = append(summaries, fmt.Sprintf("forge_version %s matches binary", pinned))
	case forgecompat.BinaryBehindPin(pinned, binv):
		// ERROR, not warn, and the only direction that gets escalated.
		//
		// A binary NEWER than the pin has every schema field the pin
		// declared, so the project merely has an upgrade pending — the
		// ordinary state between a forge release and `forge project
		// upgrade`. A binary OLDER than the pin is missing fields the
		// project is entitled to use, and since forge's KCL schema module
		// is embedded in the binary, its render fails blaming the
		// project's own line number. That is a broken install, and burying
		// it among warnings is how the original report got read as
		// "probably fine". See internal/forgecompat/skew.go.
		status = audittype.StatusError
		summaries = append(summaries,
			fmt.Sprintf("forge_version %s is pinned but the binary is %s — older than the pin, "+
				"so renders will fail against this binary's embedded KCL schemas", pinned, binv))
		details["hint"] = "install the pinned forge:\n    " +
			forgecompat.InstallCommand(pinned)
	default:
		status = audittype.StatusWarn
		summaries = append(summaries,
			fmt.Sprintf("forge_version %s does NOT match binary %s", pinned, binv))
		details["hint"] = fmt.Sprintf("run `%s project upgrade` to align the pin with the binary", cmdutil.Name())
	}

	// 2. Divergent pins: if the known pins disagree with each other, the
	//    project's "what forge built this?" answer is ambiguous. Flag it.
	//
	//    Keyed by COMMIT IDENTITY, not by string. forge writes the two pins
	//    in two vocabularies ON PURPOSE, in the same `forge project new`
	//    run: forge.yaml gets the binary's module version, while the CI pin
	//    must be resolvable by `go install`, so a dev build (whose module
	//    version is a `+dirty` pseudo-version `go install` cannot fetch)
	//    falls back to the raw git SHA. Comparing those as strings reported
	//    a PRISTINE project — created seconds earlier by this very binary —
	//    as having "divergent forge version pins", and pointed at
	//    `forge project upgrade`, which cannot converge two spellings of one
	//    commit. See pinIdentity.
	// Dedupe on identity, but REPORT the pins as written — "613340e38be3,
	// 613340e38be3" would be a useless message.
	knownPins := map[string]string{}
	addPin := func(v string) {
		if v = strings.TrimSpace(v); v != "" && v != "0.0.0" {
			if _, seen := knownPins[pinIdentity(v)]; !seen {
				knownPins[pinIdentity(v)] = v
			}
		}
	}
	addPin(cfg.ForgeVersion)
	addPin(ciPin)
	addPin(statePin)
	if len(knownPins) > 1 {
		if status == audittype.StatusOK {
			status = audittype.StatusWarn
		}
		pins := make([]string, 0, len(knownPins))
		for _, p := range knownPins {
			pins = append(pins, p)
		}
		sort.Strings(pins)
		summaries = append(summaries,
			fmt.Sprintf("divergent forge version pins across the project: %s — run `%s project upgrade` to converge them",
				strings.Join(pins, ", "), cmdutil.Name()))
	}

	return audittype.Category{
		Status:  status,
		Summary: strings.Join(summaries, "; "),
		Details: details,
	}
}

// auditShape inventories the project's structural elements: services
// (and their RPC counts), frontends, and internal packages. Workers and
// operators are owned code with no proto contract — see the comment on
// the IntrospectComponents loop below for why they are not inventoried.
func auditShape(f *factory.Factory, cfg *config.ProjectConfig, projectDir string) audittype.Category {
	if cfg == nil {
		return audittype.Category{Status: audittype.StatusError, Summary: "no forge.yaml"}
	}
	// rpcInfo is the per-RPC entry under svcInfo.RPCs. Additive
	// (introduced after rpc_count) — consumers that only read
	// rpc_count keep working.
	type rpcInfo struct {
		Name string `json:"name"`
		// Streaming is "", "client", "server", or "bidi"; omitted
		// for unary RPCs.
		Streaming string `json:"streaming,omitempty"`
		// Custom reports a `// forge:custom` marker: the rpc is custom
		// whatever its name, so forge stubs it rather than wiring CRUD.
		Custom bool `json:"custom,omitempty"`
	}
	type svcInfo struct {
		Name     string `json:"name"`
		Type     string `json:"type"`
		RPCCount int    `json:"rpc_count"`
		// RPCs lists each RPC by name with streaming info.
		// Empty when proto parsing was unavailable (see
		// proto_integrity) — additive field, may be absent.
		RPCs []rpcInfo `json:"rpcs,omitempty"`
	}
	var services []svcInfo
	var frontends []map[string]string

	// Parse RPC counts when proto/services exists. We still emit the
	// structural shape from forge.yaml so the user gets the inventory
	// even when codegen is broken — but unlike the historic silent-drop
	// behavior, we surface the parse failure as proto_integrity on the
	// details map so JSON consumers can `jq '.details.proto_integrity'`
	// and detect the "RPC count = 0 because parse failed, not because
	// the service has no methods" case. See B5 in the audit quick-win
	// backlog.
	rpcByService := map[string][]rpcInfo{}
	var protoParseErr string
	// unmanagedInfo is one `// forge:unmanaged` service: declared in proto,
	// handlers owned outside forge. Listed apart from `services` because
	// nothing forge generates or checks covers it — this is the adoption
	// ledger a project migrating onto forge works down.
	type unmanagedInfo struct {
		Name      string `json:"name"` // fully-qualified, e.g. "acme.v1.ChatService"
		ProtoFile string `json:"proto_file"`
		RPCCount  int    `json:"rpc_count"`
	}
	var unmanaged []unmanagedInfo
	// Services are read from the protoc-gen-forge descriptor, which covers
	// every Connect service in any proto package — so flat / multi-service
	// layouts (proto/controlplane/v1/*.proto) are audited identically to the
	// canonical proto/services/<svc>/v1/ layout.
	if f.Audit.ProjectDefinesConnectServices(projectDir) {
		if declared, err := codegen.ParseDeclaredServices(projectDir); err == nil {
			for _, d := range codegen.UnmanagedServices(declared) {
				unmanaged = append(unmanaged, unmanagedInfo{
					Name: d.Package + "." + d.Name, ProtoFile: d.ProtoFile, RPCCount: len(d.Methods),
				})
			}
			for _, d := range codegen.ManagedServices(declared) {
				rpcs := make([]rpcInfo, 0, len(d.Methods))
				for _, m := range d.Methods {
					// "" means unary.
					mode := ""
					switch {
					case m.ClientStreaming && m.ServerStreaming:
						mode = "bidi"
					case m.ServerStreaming:
						mode = "server"
					case m.ClientStreaming:
						mode = "client"
					}
					rpcs = append(rpcs, rpcInfo{
						Name:      m.Name,
						Streaming: mode,
						Custom:    m.Custom,
					})
				}
				rpcByService[d.Name] = rpcs
			}
		} else {
			protoParseErr = err.Error()
		}
	}

	// Services are enumerated from the proto descriptor (the authoritative,
	// non-brittle source) — see codegen.IntrospectComponents.
	// Workers/operators are owned code with no
	// proto contract; forge does not inventory them (the app names them in
	// its own wiring and enumerates them at runtime), so the shape reports
	// services only.
	for _, s := range codegen.IntrospectComponents(projectDir) {
		info := svcInfo{Name: s.Name, Type: s.EffectiveKind()}
		// match by ProtoService name suffix (Echo → EchoService)
		for protoName, rpcs := range rpcByService {
			short := strings.TrimSuffix(protoName, "Service")
			if strings.EqualFold(short, s.Name) || strings.EqualFold(protoName, s.Name) {
				info.RPCCount = len(rpcs)
				info.RPCs = rpcs
				break
			}
		}
		services = append(services, info)
	}
	for _, fe := range cfg.Frontends {
		frontends = append(frontends, map[string]string{"name": fe.Name, "type": fe.Type})
	}

	// Internal packages are DISCOVERED from internal/<pkg>/contract.go —
	// the same walk the bootstrap codegen wires from, so `packages` names
	// every contract package. `uncomposed_packages` names the ones the server
	// composition does NOT construct, read off the same markers compose.go
	// obeys: external-component (hand-built on Infra) and not-a-component
	// (not in the server at all). Absent means every package is composed. A
	// discovery failure yields an empty list; the codegen category is where a
	// broken tree gets reported.
	discoveredPkgs, _ := codegen.DiscoverInternalPackages(projectDir)
	packages := make([]string, 0, len(discoveredPkgs))
	var uncomposed []map[string]string
	for _, p := range discoveredPkgs {
		packages = append(packages, p.Name)
		if p.Composition != codegen.CompositionComposed {
			entry := map[string]string{"name": p.Name, "composition": p.Composition}
			if p.CompositionReason != "" {
				entry["reason"] = p.CompositionReason
			}
			uncomposed = append(uncomposed, entry)
		}
	}

	details := map[string]any{
		"services":  services,
		"frontends": frontends,
		"packages":  packages,
	}
	if len(uncomposed) > 0 {
		details["uncomposed_packages"] = uncomposed
	}
	// proto_integrity surfaces the RPC-count parse failure (if any) so
	// JSON consumers don't have to guess whether RPCCount: 0 means
	// "no methods" or "parse failed". Only emitted when there's an
	// error to report — under the additive-extension contract, the
	// field being absent IS the "all good" signal.
	status := audittype.StatusOK
	summary := fmt.Sprintf("kind=%s, %d service(s), %d frontend(s) (workers/operators are owned code — not inventoried by forge)",
		cfg.EffectiveKind(), len(services), len(frontends))
	if len(unmanaged) > 0 {
		sort.Slice(unmanaged, func(i, j int) bool { return unmanaged[i].Name < unmanaged[j].Name })
		details["unmanaged_services"] = unmanaged
		summary += fmt.Sprintf(", %d unmanaged service(s) (// %s — see unmanaged_services)", len(unmanaged), codegen.ProtoMarkerUnmanaged)
	}
	if protoParseErr != "" {
		details["proto_integrity"] = map[string]any{
			"status": "warn",
			"reason": "reading the forge descriptor failed — RPC counts may be 0 even for services with methods",
			"error":  protoParseErr,
		}
		status = audittype.StatusWarn
		summary += " (warn: proto parse failed — see proto_integrity)"
	}
	return audittype.Category{Status: status, Summary: summary, Details: details}
}

// auditFeatures surfaces the resolved `features:` block from forge.yaml
// at audit time. The category lists every feature gated by config —
// deploy/build/frontend/ci/docs/observability/... — and
// whether it resolves to enabled (default for nil) or disabled
// (explicit false). The additive-extension contract holds: new
// features added to config.FeaturesConfig.EffectiveFeatures() show up
// here automatically, sub-agents can branch on
// `.features.details.<name>` directly.
//
// Status is always ok — this category is informational. Sub-agents
// that care about a specific gated subsystem check the boolean in
// details; humans reading the human-formatted audit see a one-line
// "N enabled, M disabled" summary plus the per-feature breakdown.
func auditFeatures(cfg *config.ProjectConfig) audittype.Category {
	if cfg == nil {
		return audittype.Category{
			Status:  audittype.StatusError,
			Summary: "no forge.yaml — features unknown",
		}
	}
	effective := cfg.Features.EffectiveFeatures()
	// Pre-allocate to non-nil empty slices so the JSON encoder emits `[]`
	// rather than `null` when nothing falls into a bucket — sub-agents that
	// `jq '.disabled | length'` need a numeric length regardless of state.
	enabled := []string{}
	disabled := []string{}
	for name, on := range effective {
		if on {
			enabled = append(enabled, name)
		} else {
			disabled = append(disabled, name)
		}
	}
	sort.Strings(enabled)
	sort.Strings(disabled)

	details := map[string]any{
		"resolved": effective,
		"enabled":  enabled,
		"disabled": disabled,
	}
	summary := fmt.Sprintf("%d feature(s) enabled, %d disabled", len(enabled), len(disabled))
	return audittype.Category{Status: audittype.StatusOK, Summary: summary, Details: details}
}

// auditEnvironments inventories every environment declared via a
// `deploy/kcl/<env>/main.k` file. Source of truth is the filesystem —
// forge.yaml no longer declares environments. Per-env deploy info
// (cluster / namespace / registry / domain) lives in the rendered
// KCL on `forge.K8sCluster` blocks; this audit just confirms the env
// directories exist and lists them.
func auditEnvironments(f *factory.Factory, projectDir string) audittype.Category {
	envs, err := f.Audit.ListEnvs(projectDir)
	if err != nil {
		return audittype.Category{
			Status:  audittype.StatusWarn,
			Summary: fmt.Sprintf("env discovery failed: %v", err),
		}
	}
	if len(envs) == 0 {
		return audittype.Category{
			Status:  audittype.StatusOK,
			Summary: "no environments declared (no deploy/kcl/<env>/main.k)",
		}
	}
	type envEntry struct {
		Name   string `json:"name"`
		Status string `json:"status"`
	}
	entries := make([]envEntry, 0, len(envs))
	for _, env := range envs {
		entries = append(entries, envEntry{Name: env, Status: "ok"})
	}
	details := map[string]any{"environments": entries}
	return audittype.Category{
		Status:  audittype.StatusOK,
		Summary: fmt.Sprintf("%d environment(s) declared (deploy/kcl/<env>/main.k)", len(envs)),
		Details: details,
	}
}

// auditConventions runs the lint linters whose results are amenable to
// programmatic roll-up: forgeconv. Anything that requires
// shelling to a Go subprocess (golangci, contractlint) is expensive and
// noisy in an audit context — we surface a hint to run `forge lint` for
// the full picture instead.
func auditConventions(cfg *config.ProjectConfig, projectDir string) audittype.Category {
	counts := map[string]int{}
	hasErrors := false
	hasWarnings := false

	protoDir := filepath.Join(projectDir, "proto")
	if dirExists(protoDir) {
		if res, err := forgeconv.LintProtoTree(protoDir); err == nil {
			for _, f := range res.Findings {
				key := "conventions/" + string(f.Severity)
				counts[key]++
				if f.Severity == forgeconv.SeverityError {
					hasErrors = true
				} else {
					hasWarnings = true
				}
			}
		}
	}

	status := audittype.StatusOK
	switch {
	case hasErrors:
		status = audittype.StatusError
	case hasWarnings:
		status = audittype.StatusWarn
	}

	summary := "no convention violations"
	if hasErrors || hasWarnings {
		var bits []string
		keys := make([]string, 0, len(counts))
		for k := range counts {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			bits = append(bits, fmt.Sprintf("%s=%d", k, counts[k]))
		}
		summary = strings.Join(bits, ", ")
	}

	_ = cfg // reserved for future per-feature gating
	return audittype.Category{
		Status:  status,
		Summary: summary,
		Details: map[string]any{
			"counts": counts,
			"hint":   fmt.Sprintf("run `%s lint` for full output (golangci, contractlint, etc.)", cmdutil.Name()),
		},
	}
}

// auditDisownedFile is one entry of the codegen category's
// `disowned_files` detail list: a generated file the user has taken
// permanent ownership of via `forge project disown` (or a legacy fork the
// pipeline migration converted). Package-level (rather than
// function-local) so tests can assert the JSON shape directly.
type auditDisownedFile struct {
	Path string `json:"path"`
	// Since is when the file was disowned (RFC3339 UTC). Empty for
	// legacy forks whose original timestamp predates recording.
	Since string `json:"since,omitempty"`
	// Reason is the recorded WHY behind the disown, from the path's
	// .forge/disowned.json record. Additive field per the audit-json
	// contract; empty when the record predates reason capture.
	Reason string `json:"reason,omitempty"`
}

// auditCodegen reports on the project's self-certification state:
// hand-edits to forge-certified files (embedded forge:hash markers that
// fail verification), disowned files, orphan _gen files, and a pending
// legacy-manifest migration if one exists. Ownership is read from the
// files themselves — there is no global manifest to consult.
func auditCodegen(f *factory.Factory, cfg *config.ProjectConfig, projectDir string) audittype.Category {
	cs, err := generator.LoadChecksums(projectDir)
	if err != nil {
		return audittype.Category{
			Status:  audittype.StatusWarn,
			Summary: fmt.Sprintf("could not load .forge ownership state: %v", err),
		}
	}

	markers := checksums.ScanMarkers(projectDir)
	certified := len(markers) + len(cs.Unstampable)
	details := map[string]any{
		// `tracked_files` survives for audit-json consumers; its meaning
		// is now "files carrying forge's certification" (embedded marker
		// or scoped .forge/hashes.json record).
		"tracked_files":   certified,
		"certified_files": certified,
	}

	// Rough "last generate" timestamp: the newest mtime among certified
	// files (the old proxy — checksums.json's mtime — is gone with the
	// manifest).
	lastGen := time.Time{}
	for rel := range markers {
		if stat, statErr := os.Stat(filepath.Join(projectDir, rel)); statErr == nil && stat.ModTime().After(lastGen) {
			lastGen = stat.ModTime()
		}
	}
	if lastGen.IsZero() {
		details["last_generate"] = "never"
	} else {
		details["last_generate"] = lastGen.UTC().Format(time.RFC3339)
	}

	// Pending one-time migration: a legacy .forge/checksums.json still
	// present means the next `forge generate` (or `forge project upgrade`) will
	// convert it to embedded markers and delete it.
	if _, statErr := os.Stat(filepath.Join(projectDir, checksums.LegacyChecksumFile)); statErr == nil {
		details["legacy_manifest"] = "present — the next `forge generate` migrates it to embedded forge:hash markers and deletes it"
	}

	// User-edited gen files: certification fails (embedded hash doesn't
	// match the recomputed body hash). Disowned files carry no marker
	// and are excluded by construction; Tier-2-managed starters are
	// exempt (edits there are sanctioned).
	var modified []string
	modified = append(modified, f.Audit.ScanProjectDriftPaths(projectDir, cs)...)
	sort.Strings(modified)
	if len(modified) > 0 {
		details["user_edited_gen_files"] = modified
	}

	// Disowned files: one-way ownership transfers recorded in
	// .forge/disowned.json. A legitimate end state — reported for
	// visibility, never as a warning.
	var disowned []auditDisownedFile
	for rel, entry := range cs.Disowned {
		disowned = append(disowned, auditDisownedFile{Path: rel, Since: entry.DisownedAt, Reason: entry.Reason})
	}
	sort.Slice(disowned, func(i, j int) bool { return disowned[i].Path < disowned[j].Path })

	// Additive-extension contract: the legacy `forked_files` key is never
	// repurposed. It now always emits an empty array, with a note field
	// pointing consumers at its replacement; both will be dropped in a
	// future release.
	details["forked_files"] = []auditDisownedFile{}
	details["forked_files_note"] = "deprecated: the fork state was removed; see disowned_files"

	if len(disowned) > 0 {
		details["disowned_files"] = disowned
		details["disowned_hint"] = "disowned files are user-owned; forge never regenerates them. Re-adopt one by deleting it and running `forge generate`."
	}

	// Orphan _gen detection: walk the project for files ending in _gen.go
	// that carry NO certification at all. These are usually safe to
	// delete, but flagging them at audit time tells the user without
	// forcing a regenerate.
	orphans := findOrphanGenFiles(projectDir, markers, cs)
	if len(orphans) > 0 {
		details["orphan_gen_files"] = orphans
	}

	// Manifest-era `tracked_missing_files` is gone with the manifest:
	// there is no record of paths that don't exist — a deleted generated
	// file is simply re-emitted on the next generate (and deletion IS
	// the documented re-adoption signal for disowned files).
	var missing []string

	status := audittype.StatusOK
	summary := fmt.Sprintf("%d certified, %d modified, %d disowned, %d orphans, %d missing", certified, len(modified), len(disowned), len(orphans), len(missing))
	// Disowned files are a legitimate end state (unlike the old fork
	// limbo) — they appear in the summary for visibility but do NOT
	// degrade the category status.
	if len(modified) > 0 || len(orphans) > 0 || len(missing) > 0 {
		status = audittype.StatusWarn
	}
	return audittype.Category{Status: status, Summary: summary, Details: details}
}

// findOrphanGenFiles walks projectDir for *_gen.go files that carry no
// forge certification (no embedded marker, no scoped-fallback entry,
// not disowned) yet self-identify as forge output via the legacy
// banner. The walk skips noisy roots (vendor/, gen/, node_modules/,
// .git/) so audit stays cheap on large projects.
func findOrphanGenFiles(projectDir string, markers map[string]checksums.MarkerInfo, cs *generator.FileChecksums) []string {
	var orphans []string
	skip := map[string]struct{}{
		"vendor": {}, ".git": {}, "node_modules": {}, "gen": {}, ".forge": {},
		// .scratch: throwaway smoke/scratch trees (e.g. .scratch/smoke)
		// carry generated *_gen.go copies that aren't part of this project's
		// certified output. testdata: linter/generator fixtures deliberately
		// hold _gen.go files. Neither is an orphan in *this* tree.
		".scratch": {}, "testdata": {},
	}
	_ = filepath.WalkDir(projectDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if _, ok := skip[d.Name()]; ok {
				return filepath.SkipDir
			}
			return nil
		}
		name := d.Name()
		if !strings.HasSuffix(name, "_gen.go") && !strings.HasSuffix(name, "_gen_test.go") {
			return nil
		}
		rel, err := filepath.Rel(projectDir, path)
		if err != nil {
			return nil
		}
		relSlash := filepath.ToSlash(rel)
		if _, ok := markers[relSlash]; ok {
			return nil // certified — owned and accounted for
		}
		if cs != nil {
			if cs.IsDisowned(relSlash) {
				return nil
			}
			if _, ok := cs.Unstampable[relSlash]; ok {
				return nil
			}
		}
		// Banner check: forge-banner files generated by pre-marker forge
		// versions that never got certified.
		if isForgeGeneratedBanner(path) {
			orphans = append(orphans, relSlash)
		}
		return nil
	})
	sort.Strings(orphans)
	return orphans
}

// isForgeGeneratedBanner returns true when the file's first line
// declares it as "Code generated by forge" — the legacy authorship
// marker from before embedded forge:hash certification.
func isForgeGeneratedBanner(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer func() { _ = f.Close() }()
	buf := make([]byte, 256)
	n, _ := f.Read(buf)
	head := string(buf[:n])
	if i := strings.IndexByte(head, '\n'); i >= 0 {
		head = head[:i]
	}
	return strings.Contains(head, "Code generated by forge")
}

// auditScaffoldMarkers counts unfilled FORGE_SCAFFOLD placeholders that
// have survived a commit. The semantic check matches `forge lint
// --scaffolds`: only line-start `// FORGE_SCAFFOLD:` comments count as
// real markers; mere references to the literal string in source, docs,
// or template bodies do not. Directories that are intentional homes for
// markers (linter testdata, project templates) are skipped, matching
// the lint walker's skipDir + scaffold-template carve-outs.
//
// Without this filter, the audit would warn on every project that
// includes the scaffold linter source itself, the analyzer fixtures, or
// the generator templates that EMIT markers — i.e. it would be noisy on
// forge's own tree (which is a forge-managed project).
func auditScaffoldMarkers(projectDir string) audittype.Category {
	skip := map[string]struct{}{
		"vendor": {}, ".git": {}, "node_modules": {}, "gen": {}, ".forge": {},
		// testdata: linter fixtures intentionally hold markers so the
		// analyzer suite can assert it fires on them.
		"testdata": {},
		// .scratch: throwaway smoke/scratch trees (e.g. .scratch/smoke)
		// carry scaffold markers from generated fixtures — not unfilled
		// placeholders in *this* tree.
		".scratch": {},
		// templates/: forge's own scaffold templates contain the
		// markers as their literal output — they're not unfilled
		// placeholders in *this* tree.
		"templates": {},
	}
	var files []string
	total := 0
	_ = filepath.WalkDir(projectDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if _, ok := skip[d.Name()]; ok {
				return filepath.SkipDir
			}
			return nil
		}
		// Only scan files whose markers would be unfilled placeholders
		// (Go/proto/TS/YAML/SQL/templates). Markdown and JSON often
		// reference the marker syntax verbatim for documentation; we
		// don't want every README that mentions `FORGE_SCAFFOLD:` to
		// turn audit yellow.
		if !cmdutil.IsMarkerScannable(d.Name()) {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		count := cmdutil.CountLineStartScaffoldMarkers(data)
		if count > 0 {
			rel, _ := filepath.Rel(projectDir, path)
			files = append(files, filepath.ToSlash(rel))
			total += count
		}
		return nil
	})
	sort.Strings(files)
	status := audittype.StatusOK
	if total > 0 {
		status = audittype.StatusWarn
	}
	return audittype.Category{
		Status:  status,
		Summary: fmt.Sprintf("%d FORGE_SCAFFOLD marker(s) across %d file(s)", total, len(files)),
		Details: map[string]any{"files": files, "total_markers": total},
	}
}

// auditCRUDStubs counts custom-read-shape CodeUnimplemented stubs in
// the project's CRUD shim files (user-owned handlers_crud.go, plus
// legacy handlers_crud_gen.go from pre-split projects). These stubs are
// scaffolded when a request/response message shape deliberately
// diverges from the AIP-158 CRUD conventions — a legitimate domain
// decision, not an error; the body is the user's to implement in the
// owned shim. The stub keeps the file compiling but returns
// CodeUnimplemented at runtime — production traffic to that RPC will
// 501 until the body lands, which is why the category warns.
//
// Markers recognized: `// forge:custom-read-shape: <reason>` (current)
// and `// FORGE_CRUD_SHAPE_MISMATCH: <reason>` (the pre-rename
// spelling, kept for one release so existing files and greps keep
// matching — see the legacy_marker detail).
//
// Without this audit, the stub is silent: nothing in `forge doctor`
// or CI tells the operator that an RPC is shipping as Unimplemented
// (forge generate prints one warning line per stub at scaffold time).
// This category surfaces the count + per-method list as a structured
// audit finding so CI can branch on `.crud_stubs.status == "warn"`.
//
// We scan files matching the CRUD shim names rather than every Go file —
// both to keep the walk cheap and because the marker is forge-emitted
// and lives only in those files. Skip set mirrors auditScaffoldMarkers
// (vendor/.git/etc) plus templates/ and testdata/ so forge's own tree
// doesn't false-positive on its template body (which contains the
// literal marker as emission text).
func auditCRUDStubs(projectDir string) audittype.Category {
	skip := map[string]struct{}{
		"vendor": {}, ".git": {}, "node_modules": {}, "gen": {}, ".forge": {},
		"testdata":  {},
		"templates": {},
	}
	var stubs []map[string]string
	files := map[string]int{}
	total := 0
	_ = filepath.WalkDir(projectDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if _, ok := skip[d.Name()]; ok {
				return filepath.SkipDir
			}
			return nil
		}
		if d.Name() != "handlers_crud.go" && d.Name() != "handlers_crud_gen.go" {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		rel, _ := filepath.Rel(projectDir, path)
		rel = filepath.ToSlash(rel)
		// Walk lines so each marker carries the nearest preceding
		// `func (s *Service) <Method>(` name — callers want the RPC
		// identifier, not just a file path. The CRUD template emits
		// the forge:custom-read-shape comment between the doc
		// comment and the function declaration, so we record the
		// method name when we *next* see the func line. Simpler:
		// stash the previous func name and apply it to the next
		// marker we see (the marker sits above the matching func).
		lines := strings.Split(string(data), "\n")
		type pendingMarker struct {
			reason string
			idx    int
		}
		var pending []pendingMarker
		for i, line := range lines {
			trimmed := strings.TrimLeft(line, " \t")
			if marker, ok := strings.CutPrefix(trimmed, "// forge:custom-read-shape:"); ok {
				pending = append(pending, pendingMarker{reason: strings.TrimSpace(marker), idx: i})
				continue
			}
			// Pre-rename spelling — recognized for one release.
			if marker, ok := strings.CutPrefix(trimmed, "// FORGE_CRUD_SHAPE_MISMATCH:"); ok {
				pending = append(pending, pendingMarker{reason: strings.TrimSpace(marker), idx: i})
				continue
			}
			if !strings.HasPrefix(trimmed, "func (s *Service) ") {
				continue
			}
			if len(pending) == 0 {
				continue
			}
			rest := strings.TrimPrefix(trimmed, "func (s *Service) ")
			methodEnd := strings.IndexAny(rest, "(")
			if methodEnd <= 0 {
				pending = pending[:0]
				continue
			}
			methodName := rest[:methodEnd]
			// Attach every pending marker to this func (in practice
			// the template emits exactly one per func, but the loop
			// stays correct if that changes).
			for _, p := range pending {
				stubs = append(stubs, map[string]string{
					"file":   rel,
					"method": methodName,
					"reason": p.reason,
				})
				files[rel]++
				total++
			}
			pending = pending[:0]
		}
		return nil
	})
	// Stable file list for the summary.
	fileNames := make([]string, 0, len(files))
	for f := range files {
		fileNames = append(fileNames, f)
	}
	sort.Strings(fileNames)
	// Deterministic stubs order (by file, then method) so JSON diffs are clean.
	sort.SliceStable(stubs, func(i, j int) bool {
		if stubs[i]["file"] != stubs[j]["file"] {
			return stubs[i]["file"] < stubs[j]["file"]
		}
		return stubs[i]["method"] < stubs[j]["method"]
	})
	status := audittype.StatusOK
	summary := "0 custom-read-shape CRUD stubs"
	if total > 0 {
		status = audittype.StatusWarn
		summary = fmt.Sprintf("%d custom-read-shape CRUD stub(s) across %d file(s) — bodies are yours to implement; the RPCs return CodeUnimplemented until then", total, len(fileNames))
	}
	return audittype.Category{
		Status:  status,
		Summary: summary,
		Details: map[string]any{
			"files":       fileNames,
			"total_stubs": total,
			"stubs":       stubs,
			// Grep compatibility: the marker was renamed from
			// FORGE_CRUD_SHAPE_MISMATCH to forge:custom-read-shape;
			// both spellings are scanned this release. Consumers
			// grepping source for the old string should switch.
			"marker":        "forge:custom-read-shape",
			"legacy_marker": "FORGE_CRUD_SHAPE_MISMATCH",
		},
	}
}

// auditMigrationSafety summarises the project's migration safety posture:
// the migration count, the destructive_change severity, which migrations
// carry a `-- forge:allow-destructive` directive, and the timestamp of the
// most recent migration. Surfaces as warn when any migration carries the
// directive (informational — someone consciously opted that file out of the
// destructive-change guard, and it is worth re-reviewing).
func auditMigrationSafety(cfg *config.ProjectConfig, projectDir string) audittype.Category {
	migDir := filepath.Join(projectDir, "db", "migrations")
	if cfg != nil && cfg.Database.MigrationsDir != "" {
		migDir = filepath.Join(projectDir, cfg.Database.MigrationsDir)
	}
	hasMigrations := dirExists(migDir)
	if !hasMigrations {
		return audittype.Category{Status: audittype.StatusOK, Summary: "no migrations directory (n/a)"}
	}

	// Count migrations + find the latest mtime.
	var latestMtime time.Time
	latestName := ""
	migCount := 0
	entries, _ := os.ReadDir(migDir)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".up.sql") {
			continue
		}
		migCount++
		info, err := e.Info()
		if err != nil {
			continue
		}
		if info.ModTime().After(latestMtime) {
			latestMtime = info.ModTime()
			latestName = e.Name()
		}
	}

	details := map[string]any{
		"migration_count": migCount,
		"migrations_dir":  migDir,
	}
	if latestName != "" {
		details["latest_migration"] = latestName
		details["latest_migration_mtime"] = latestMtime.UTC().Format(time.RFC3339)
	}

	allowed := migrationlint.AllowDestructiveFiles(migDir)
	severity := "error"
	if cfg != nil {
		severity = cfg.Database.MigrationSafety.EffectiveDestructiveChange()
	}
	details["destructive_change_severity"] = severity
	if len(allowed) > 0 {
		details["allow_destructive_files"] = allowed
	}

	status := audittype.StatusOK
	summary := fmt.Sprintf("%d migration(s); %d marked -- forge:allow-destructive; destructive_change=%s",
		migCount, len(allowed), severity)
	if len(allowed) > 0 {
		// Informational warn — surface that the project has explicit
		// destructive carve-outs the user should re-review periodically.
		status = audittype.StatusWarn
		details["hint"] = "review the migrations marked -- forge:allow-destructive periodically; the directive is the one exemption from the destructive-change guard"
	}
	return audittype.Category{Status: status, Summary: summary, Details: details}
}

// scanCategory captures the shape every lint-backed audit collector
// shares: run a scan, fold a scan error into a warn, report an empty
// result as ok, and otherwise hand the findings to a rollup. Only the
// scanErrPrefix (the text before ": <err>") and okMsg vary across
// collectors; the warn/ok category shells are byte-identical so they
// live here once. onFindings owns the non-empty case — it never sees a
// nil/empty slice.
func scanCategory[F any](scan func() ([]F, error), scanErrPrefix, okMsg string, onFindings func([]F) audittype.Category) audittype.Category {
	findings, err := scan()
	if err != nil {
		return audittype.Category{
			Status:  audittype.StatusWarn,
			Summary: fmt.Sprintf("%s: %v", scanErrPrefix, err),
		}
	}
	if len(findings) == 0 {
		return audittype.Category{
			Status:  audittype.StatusOK,
			Summary: okMsg,
		}
	}
	return onFindings(findings)
}

// rollupByPackage aggregates findings into the <role>/<pkg>-keyed
// breakdown shared (byte-for-byte) by the optional-deps-guard and
// config-deps collectors: bucket each finding under keyFn(f), render its
// per-line detail via lineFn(f), sort the package keys, and emit the
// warn category with the canonical {finding_count, affected_packages,
// by_package, hint} details. keyFn typically returns `f.Role + "/" +
// f.Package`; summaryFn renders the one-line summary from the finding /
// package counts.
func rollupByPackage[F any](findings []F, keyFn func(F) string, lineFn func(F) string, summaryFn func(findingCount, pkgCount int) string, hint string) audittype.Category {
	byPackage := map[string][]string{}
	for _, f := range findings {
		key := keyFn(f)
		byPackage[key] = append(byPackage[key], lineFn(f))
	}
	pkgs := make([]string, 0, len(byPackage))
	for k := range byPackage {
		pkgs = append(pkgs, k)
	}
	sort.Strings(pkgs)

	return audittype.Category{
		Status:  audittype.StatusWarn,
		Summary: summaryFn(len(findings), len(pkgs)),
		Details: map[string]any{
			"finding_count":     len(findings),
			"affected_packages": pkgs,
			"by_package":        byPackage,
			"hint":              hint,
		},
	}
}

// auditOptionalDepsGuard rolls up unguarded derefs of
// `// forge:optional-dep` Deps fields (the optional-deps-guard lint).
// Optional fields skip validateDeps by design, so an unguarded
// `s.deps.X.Method(...)` is a latent nil-panic no startup gate catches.
// Findings are warn-level (the walker is conservative, not full
// dataflow — see lint_optional_deps_guard.go); the category is
// additive per the audit-json contract (consumers iterate
// `.categories | keys[]` and tolerate new entries).
func auditOptionalDepsGuard(projectDir string) audittype.Category {
	// Aggregate by <role>/<pkg> so a sub-agent can jump straight to the
	// offending component; per-finding detail lives in
	// `forge lint --optional-deps-guard --json`.
	return scanCategory(
		func() ([]lint.OptionalDepsGuardFinding, error) {
			return lint.CollectOptionalDepsGuardFindings(projectDir)
		},
		"optional-deps-guard scan failed",
		"optional-deps-guard clean — every optional-dep deref is nil-guarded (or suppressed)",
		func(findings []lint.OptionalDepsGuardFinding) audittype.Category {
			return rollupByPackage(findings,
				func(f lint.OptionalDepsGuardFinding) string { return f.Role + "/" + f.Package },
				func(f lint.OptionalDepsGuardFinding) string {
					return fmt.Sprintf("%s:%d %s in %s", f.File, f.Line, f.Expr, f.Method)
				},
				func(findingCount, pkgCount int) string {
					return fmt.Sprintf("%d unguarded optional-dep deref(s) across %d package(s)", findingCount, pkgCount)
				},
				fmt.Sprintf("run `%s lint --optional-deps-guard` for the full per-line report; suppress confirmed-safe sites with `// forge:optional-checked` on the deref line", cmdutil.Name()),
			)
		},
	)
}

// auditConfigDeps rolls up scalar Deps fields (the config-deps lint).
// Scalar Deps fields are configuration, not collaborators — wire_gen
// can never resolve them from App/AppExtras, so they regenerate as
// typed zeros + TODOs forever (kalshi-trader fr-ad24278452) unless the
// user hand-projects them via AppExtras + setup.go. The supported
// shape is a component config block in proto/<project>/config/v1 taken as one typed
// field. Findings are warn-level; the category is additive per the
// audit-json contract (consumers iterate `.categories | keys[]` and
// tolerate new entries).
func auditConfigDeps(projectDir string) audittype.Category {
	// Aggregate by <role>/<pkg> so a sub-agent can jump straight to the
	// offending component; per-finding detail lives in
	// `forge lint --config-deps --json`.
	return scanCategory(
		func() ([]lint.ConfigDepsFinding, error) { return lint.CollectConfigDepsFindings(projectDir) },
		"config-deps scan failed",
		"config-deps clean — no scalar Deps fields (configuration flows through config blocks)",
		func(findings []lint.ConfigDepsFinding) audittype.Category {
			return rollupByPackage(findings,
				func(f lint.ConfigDepsFinding) string { return f.Role + "/" + f.Package },
				func(f lint.ConfigDepsFinding) string {
					return fmt.Sprintf("%s:%d Deps.%s %s", f.File, f.Line, f.Field, f.Type)
				},
				func(findingCount, pkgCount int) string {
					return fmt.Sprintf("%d scalar Deps field(s) across %d package(s) — scalars are configuration; declare component config blocks in proto/<project>/config/v1", findingCount, pkgCount)
				},
				fmt.Sprintf("run `%s lint --config-deps` for per-field remediation snippets (declare a <Component>Config message in proto/<project>/config/v1/config.proto and take it as `Cfg *config.<Component>Config`)", cmdutil.Name()),
			)
		},
	)
}

// auditDeps surfaces dep-shaped risks: missing go.sum, gen/ unstable
// (would `go mod tidy` produce a diff?). We don't actually run `go mod
// tidy` because that's expensive and would mutate state — we just check
// for go.sum presence and report.
func auditDeps(projectDir string) audittype.Category {
	details := map[string]any{}
	hasWarn := false

	if _, err := os.Stat(filepath.Join(projectDir, "go.mod")); err == nil {
		details["go_mod"] = "present"
		if _, err := os.Stat(filepath.Join(projectDir, "go.sum")); os.IsNotExist(err) {
			details["go_sum"] = "missing — run `go mod tidy`"
			hasWarn = true
		} else {
			details["go_sum"] = "present"
		}
	} else {
		details["go_mod"] = "missing"
	}

	if _, err := os.Stat(filepath.Join(projectDir, "gen", "go.mod")); err == nil {
		details["gen_go_mod"] = "present"
	}

	status := audittype.StatusOK
	summary := "deps look healthy"
	if hasWarn {
		status = audittype.StatusWarn
		summary = "deps need attention"
	}
	return audittype.Category{Status: status, Summary: summary, Details: details}
}

// File / method-count thresholds. Chosen to flag the genuine monster
// files (FORGE_SHAPE_REDESIGN §6d: internal/db/postgres.go ~5,354 LOC /
// 166 methods) without nagging on normal large files. Warn-level only —
// splitting is app work, the audit's job is to make the navigation
// hazard visible to an LLM/operator, not to gate the build.
const (
	auditFileLineWarn   = 1500 // a .go file past this is hard to navigate
	auditTypeMethodWarn = 40   // a receiver type with this many methods is a god-object
)

// auditFileSizeWalkSkip are directory names the size walk skips — generated
// output, vendored deps, VCS, node_modules. Generated files (gen/) are
// excluded because their size is forge's concern, not the user's, and they
// are not hand-navigated.
var auditFileSizeWalkSkip = map[string]struct{}{
	"vendor": {}, ".git": {}, "node_modules": {}, "gen": {}, ".forge": {}, "testdata": {},
}

// auditFileSizes flags oversized Go files and god-object types (receiver
// types with a high method count) — the LLM-navigation hazard from
// FORGE_SHAPE_REDESIGN §6d. App-specific to fix (splitting is the user's
// call), so it is purely ADVISORY: the category always reports status ok
// and NEVER gates Overall. It surfaces the hazard in one place an agent
// can branch on (`.file_sizes.details.oversized_files`), without turning a
// "could be tidier" hint into a build-blocking warn.
//
// Counts physical lines per file and methods per receiver type (via a
// cheap AST parse). _gen.go files are scanned for line count; _test.go
// files are excluded from BOTH metrics (table-driven test files are
// legitimately large and carry many funcs), and _gen.go is additionally
// excluded from the method-count god-object check.
func auditFileSizes(projectDir string) audittype.Category {
	type bigFile struct {
		Path  string `json:"path"`
		Lines int    `json:"lines"`
	}
	type bigType struct {
		Path    string `json:"path"`
		Type    string `json:"type"`
		Methods int    `json:"methods"`
	}
	var bigFiles []bigFile
	var bigTypes []bigType

	_ = filepath.WalkDir(projectDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil
		}
		if d.IsDir() {
			if _, ok := auditFileSizeWalkSkip[d.Name()]; ok {
				return filepath.SkipDir
			}
			return nil
		}
		if !strings.HasSuffix(d.Name(), ".go") {
			return nil
		}
		data, rerr := os.ReadFile(path)
		if rerr != nil {
			return nil
		}
		rel, _ := filepath.Rel(projectDir, path)
		rel = filepath.ToSlash(rel)

		lines := 1 + strings.Count(string(data), "\n")
		// Exclude _test.go from the oversized metric: table-driven test
		// files are legitimately large (they encode many cases, not tangled
		// logic), and the metric is a hand-navigation hint for production
		// code. Counting them just trains the reader to ignore the category.
		if lines >= auditFileLineWarn && !strings.HasSuffix(d.Name(), "_test.go") {
			bigFiles = append(bigFiles, bigFile{Path: rel, Lines: lines})
		}

		// Method-count god-object check skips generated + test files.
		if strings.HasSuffix(d.Name(), "_gen.go") || strings.HasSuffix(d.Name(), "_test.go") {
			return nil
		}
		for typeName, count := range methodCountsByReceiver(data) {
			if count >= auditTypeMethodWarn {
				bigTypes = append(bigTypes, bigType{Path: rel, Type: typeName, Methods: count})
			}
		}
		return nil
	})

	sort.Slice(bigFiles, func(i, j int) bool { return bigFiles[i].Lines > bigFiles[j].Lines })
	sort.Slice(bigTypes, func(i, j int) bool { return bigTypes[i].Methods > bigTypes[j].Methods })

	// Advisory only: splitting a big file aids navigation but is not a
	// defect, so this category NEVER gates Overall — it stays ok and simply
	// prints the list. (Contrast crud_stubs / scaffold_markers, which flag
	// runtime-incorrect state and legitimately warn.) The status stays OK
	// even when files are listed; consumers that want the hint read
	// `.file_sizes.details.oversized_files` directly.
	summary := fmt.Sprintf("no files over %d lines or types over %d methods", auditFileLineWarn, auditTypeMethodWarn)
	if len(bigFiles) > 0 || len(bigTypes) > 0 {
		summary = fmt.Sprintf("%d oversized file(s) (>%d lines), %d god-object type(s) (>%d methods) — advisory: splitting aids LLM navigation (non-gating)",
			len(bigFiles), auditFileLineWarn, len(bigTypes), auditTypeMethodWarn)
	}
	return audittype.Category{
		Status:  audittype.StatusOK,
		Summary: summary,
		Details: map[string]any{
			"line_threshold":   auditFileLineWarn,
			"method_threshold": auditTypeMethodWarn,
			"oversized_files":  bigFiles,
			"god_object_types": bigTypes,
			"advisory":         true,
		},
	}
}

// methodCountsByReceiver parses Go source and returns receiver-type name →
// method count (methods declared with a receiver `(r *T)` / `(r T)`).
// Parse failures yield an empty map — the size audit is best-effort and
// never fails the run on a mid-edit file.
func methodCountsByReceiver(src []byte) map[string]int {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "", src, 0)
	if err != nil {
		return nil
	}
	counts := map[string]int{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || len(fn.Recv.List) == 0 {
			continue
		}
		name := receiverTypeName(fn.Recv.List[0].Type)
		if name != "" {
			counts[name]++
		}
	}
	return counts
}

// receiverTypeName extracts the base type name from a receiver expr,
// unwrapping a leading pointer (`*T` → "T") and ignoring generic type
// parameters (`T[K]` → "T").
func receiverTypeName(expr ast.Expr) string {
	switch t := expr.(type) {
	case *ast.StarExpr:
		return receiverTypeName(t.X)
	case *ast.Ident:
		return t.Name
	case *ast.IndexExpr: // generic receiver T[K]
		return receiverTypeName(t.X)
	case *ast.IndexListExpr: // generic receiver T[K, V]
		return receiverTypeName(t.X)
	}
	return ""
}

// unwiredStubMarkerRE matches the per-RPC marker line
// `// forge:gen unwired-stub symbol=<pkg>.<Method>` that every unwired-stub
// emitter stamps on an un-implemented Connect handler. Audit uses it for the
// line-level lookback that classifies a method as stub-or-real; attributing
// markers to method names is codegen.ScanUnwiredStubMethods' job. The
// canonical definition lives in codegen, alongside the marker every emitter
// builds from, so audit and the emitters can never drift on what counts as
// an unwired stub.
var unwiredStubMarkerRE = codegen.UnwiredStubMarkerRE

// auditOrphanStubs flags services whose handler methods are ALL still
// un-implemented stubs (every RPC returns Unimplemented, carrying the
// `// forge:gen unwired-stub` marker every stub emitter stamps). These are
// the zero-implementation orphans from FORGE_SHAPE_REDESIGN §7f: a
// `forge scaffold service` scaffold nobody ever filled in, shipping 501s if
// served. A service with at least one real (marker-free) handler method is
// NOT flagged — partial implementation is normal mid-development.
//
// Retirement path: `forge project delete service <name>` (which tombstones it
// types-only), or implement the handler bodies. Warn-level — an orphan
// stub is a smell, not a build break.
func auditOrphanStubs(f *factory.Factory, cfg *config.ProjectConfig, projectDir string) audittype.Category {
	handlersDir := filepath.Join(projectDir, "internal", "handlers")
	entries, err := os.ReadDir(handlersDir)
	if err != nil {
		// No handlers dir (library/CLI project or pre-scaffold) — n/a.
		return audittype.Category{Status: audittype.StatusOK, Summary: "no internal/handlers/ directory (n/a)"}
	}

	type orphan struct {
		Service     string   `json:"service"`
		Dir         string   `json:"dir"`
		StubMethods []string `json:"stub_methods"`
	}
	var orphans []orphan

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(handlersDir, e.Name())
		stubMethods, realMethods := scanHandlerStubs(dir)
		// Orphan = at least one stub method AND zero real (implemented)
		// handler methods. A service with no handler methods at all (no
		// RPCs yet) is not an orphan stub — there's nothing un-implemented.
		if len(stubMethods) == 0 || realMethods > 0 {
			continue
		}
		sort.Strings(stubMethods)
		orphans = append(orphans, orphan{
			Service:     e.Name(),
			Dir:         "internal/handlers/" + e.Name(),
			StubMethods: stubMethods,
		})
	}
	sort.Slice(orphans, func(i, j int) bool { return orphans[i].Service < orphans[j].Service })

	status := audittype.StatusOK
	summary := "no zero-implementation orphan-stub services"
	if len(orphans) > 0 {
		status = audittype.StatusWarn
		summary = fmt.Sprintf("%d service(s) with ALL handlers un-implemented (CodeUnimplemented) — implement them or run `%s project delete service <name>` to retire", len(orphans), cmdutil.Name())
	}
	return audittype.Category{
		Status:  status,
		Summary: summary,
		Details: map[string]any{
			"orphan_services": orphans,
			"hint":            fmt.Sprintf("a zero-impl service serves 501s if registered; implement the handler bodies or `%s project delete service <name>`", cmdutil.Name()),
		},
	}
}

// scanHandlerStubs walks a handler directory's non-test .go files and
// returns (stub method names, count of real handler methods). A stub is a
// method preceded by the `// forge:gen unwired-stub` marker; a "real"
// handler method is a func with a receiver whose body is NOT a forge stub.
// We approximate "real handler method" as: a method declared in
// rpc_<name>.go / handlers_crud.go (the user-owned handler files) whose
// immediately-preceding line is NOT the unwired-stub marker. This keeps the
// scan a cheap line+AST pass without resolving the Connect interface.
func scanHandlerStubs(dir string) (stubMethods []string, realMethods int) {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil, 0
	}
	// Which methods are marked stubs is codegen's question — it owns the
	// marker every emitter stamps and the scanner that reads it back. Audit
	// only adds the counterpart the orphan verdict needs: how many methods
	// in the same directory are NOT stubs.
	stubSet := codegen.ScanUnwiredStubMethods(dir)
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".go") || strings.HasSuffix(e.Name(), "_test.go") {
			continue
		}
		// Only the hand-written handler files carry real-vs-stub method
		// bodies; _gen.go files are forge-owned wiring, not impl.
		if strings.HasSuffix(e.Name(), "_gen.go") {
			// Still mine _gen files for stub markers? No: the marker lives
			// in the Tier-2 handler scaffolds. Skip generated files.
			continue
		}
		data, rerr := os.ReadFile(filepath.Join(dir, e.Name()))
		if rerr != nil {
			continue
		}
		// Count receiver methods that are NOT preceded by a stub marker —
		// those are implemented handler bodies (or helpers). We use the
		// AST for method decls and the marker set for stub classification.
		for typeName, names := range handlerMethodNames(data) {
			_ = typeName
			for _, n := range names {
				if !markerPrecedesMethod(data, n) {
					realMethods++
				}
			}
		}
	}
	for m := range stubSet {
		stubMethods = append(stubMethods, m)
	}
	// A method counted as "real" only because its marker lives in a
	// different file would be a false negative; in practice the marker and
	// the method body are co-located in the same handler file, so this is sound for
	// the orphan (ALL-stub) determination.
	return stubMethods, realMethods
}

// handlerMethodNames returns receiver-type → method names for exported
// methods (handler RPC methods are exported). Parse failure → empty.
func handlerMethodNames(src []byte) map[string][]string {
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, "", src, 0)
	if err != nil {
		return nil
	}
	out := map[string][]string{}
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Recv == nil || len(fn.Recv.List) == 0 || !fn.Name.IsExported() {
			continue
		}
		recv := receiverTypeName(fn.Recv.List[0].Type)
		out[recv] = append(out[recv], fn.Name.Name)
	}
	return out
}

// markerPrecedesMethod reports whether method `name`'s declaration is
// immediately preceded (within its doc-comment block) by the
// unwired-stub marker — i.e. this method is a forge stub. Approximated by
// finding the `func (... ) <name>(` line and checking the few lines above
// it for the marker. Cheap and robust enough for the ALL-stub orphan
// determination.
func markerPrecedesMethod(src []byte, name string) bool {
	lines := strings.Split(string(src), "\n")
	funcRE := regexp.MustCompile(`^func\s*\([^)]*\)\s*` + regexp.QuoteMeta(name) + `\s*\(`)
	for i, line := range lines {
		if !funcRE.MatchString(strings.TrimLeft(line, " \t")) {
			continue
		}
		// Look back up to 4 lines (doc comment) for the marker.
		for j := i - 1; j >= 0 && j >= i-4; j-- {
			if unwiredStubMarkerRE.MatchString(lines[j]) {
				return true
			}
			// Stop at a blank line or non-comment (end of doc block).
			t := strings.TrimSpace(lines[j])
			if t == "" || !strings.HasPrefix(t, "//") {
				break
			}
		}
		return false
	}
	return false
}

// printAuditReport renders the human-readable audit. Layout: one line
// header, then one block per category in auditCategoryOrder, then a
// trailing overall verdict.
func printAuditReport(w io.Writer, r *Report) {
	_, _ = fmt.Fprintf(w, "Forge audit — %s (kind=%s, binary=%s)\n", r.ProjectName, r.ProjectKind, r.BinaryVersion)
	_, _ = fmt.Fprintf(w, "Generated at %s\n\n", r.GeneratedAt.Format(time.RFC3339))

	printed := map[string]struct{}{}
	for _, key := range auditCategoryOrder {
		if cat, ok := r.Categories[key]; ok {
			printAuditCategory(w, key, cat)
			printed[key] = struct{}{}
		}
	}
	// Fallthrough: any categories not in the canonical order, alphabetical.
	var extras []string
	for k := range r.Categories {
		if _, ok := printed[k]; !ok {
			extras = append(extras, k)
		}
	}
	sort.Strings(extras)
	for _, k := range extras {
		printAuditCategory(w, k, r.Categories[k])
	}

	_, _ = fmt.Fprintf(w, "Overall: %s\n", strings.ToUpper(string(r.OverallStatus)))
}

func printAuditCategory(w io.Writer, key string, cat audittype.Category) {
	icon := "✓"
	switch cat.Status {
	case audittype.StatusWarn:
		icon = "⚠"
	case audittype.StatusError:
		icon = "✗"
	}
	_, _ = fmt.Fprintf(w, "%s %s — %s\n", icon, key, cat.Summary)
	if len(cat.Details) > 0 {
		// Print details indented; primitives inline, slices/maps collapsed.
		keys := make([]string, 0, len(cat.Details))
		for k := range cat.Details {
			keys = append(keys, k)
		}
		sort.Strings(keys)
		for _, k := range keys {
			v := cat.Details[k]
			_, _ = fmt.Fprintf(w, "    %s: %s\n", k, formatDetailValue(v))
		}
	}
	printScopingRemediations(w, cat)
	_, _ = fmt.Fprintln(w)
}

// printScopingRemediations prints each gating finding's owner-scoping
// wrapper in full, below the detail lines.
//
// It exists because formatDetailValue JSON-marshals a slice and truncates
// it at 200 characters, and a scoping wrapper is an order of magnitude
// longer — so the remediation carried in the report was present in
// --json and invisible on the terminal where the refusal was actually
// read. Telling that reader to re-run with --json to obtain the fix is
// the same dead end in a smaller form: forge refuses in one place and
// keeps the remedy in another.
//
// Only the gating set is printed. An advisory finding carries no
// wrapper, so nothing is emitted and every other category renders
// exactly as before.
func printScopingRemediations(w io.Writer, cat audittype.Category) {
	gating, ok := cat.Details["owner_scoped_unscoped_rpcs"].([]unscopedRPC)
	if !ok {
		return
	}
	for _, g := range gating {
		if g.Remediation == "" {
			continue
		}
		_, _ = fmt.Fprintf(w, "\n    ── %s.%s — paste into %s ──\n", g.Service, g.Method, g.File)
		for _, line := range strings.Split(g.Remediation, "\n") {
			_, _ = fmt.Fprintf(w, "    %s\n", line)
		}
	}
}

func formatDetailValue(v any) string {
	switch t := v.(type) {
	case string:
		return t
	case []string:
		if len(t) == 0 {
			return "[]"
		}
		if len(t) <= 5 {
			return "[" + strings.Join(t, ", ") + "]"
		}
		return fmt.Sprintf("[%s, ... (%d total)]", strings.Join(t[:5], ", "), len(t))
	default:
		b, err := json.Marshal(v)
		if err != nil {
			return fmt.Sprintf("%v", v)
		}
		s := string(b)
		if len(s) > 200 {
			s = s[:197] + "..."
		}
		return s
	}
}
