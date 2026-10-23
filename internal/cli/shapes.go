package cli

import (
	"bufio"
	"fmt"
	"go/ast"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"

	"github.com/spf13/cobra"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/sqlident"
	"github.com/reliant-labs/forge/kcl"
)

// `forge project shapes` — the symbol index an agent needs for recon, derived
// LIVE from the sources on every invocation.
//
// ── Why this exists ──────────────────────────────────────────────────────
//
// Measured across two forge-one-shot runs: fan-out agents spent 28.4 minutes
// across 123 tool calls answering "where is X and what shape is it" with
// `find` / `grep` / `ls`. It was the single largest recoverable cost in the
// run, larger than any generator gap. The dominant pattern was paging one
// 1,685-line proto by symbol, one message at a time:
//
//	grep -n "message ListMaterialsRequest" -A 10 proto/services/roofops/v1/roofops.proto
//	grep -n "message Material {" -A 20 ...
//
// Eleven agents each re-derived the same contract, and each grep cost a whole
// turn. One `shapes` call answers all of it.
//
// ── Why it does NOT read gen/forge_descriptor.json ───────────────────────
//
// The descriptor is a derived CACHE of the protos. A command whose whole job
// is to tell an agent the truth must not answer from a cache that can be
// stale — and that cache demonstrably could: renaming one RPC in it made
// `forge project graph` report an RPC present in no proto file. Parsing the
// sources costs milliseconds and cannot lie. Recon output that is confidently
// wrong is worse than no recon output, because nothing downstream re-checks it.
//
// ── Output ───────────────────────────────────────────────────────────────
//
// One symbol per line, greppable and diffable, always with file:line so the
// next step is a targeted read rather than another search:
//
//	rpc      roofops.RecalculateEstimate   proto/services/roofops/v1/roofops.proto:412  in=… out=…
//	rpc      legacy.Ping                   proto/services/legacy/v1/legacy.proto:9  in=… out=… unmanaged
//	message  Estimate                      proto/services/roofops/v1/roofops.proto:640  fields=14
//	enum     EstimateStatus                proto/services/roofops/v1/roofops.proto:604  values=5
//	table    estimates                     db/migrations/00004_create_estimates.up.sql:9
//	store    EstimateStore                 internal/db/store_gen.go:214  go doc ./internal/db EstimateStore
//	handler  RoofopsService.RecalculateEstimate internal/handlers/roofops/rpc_recalculate_estimate.go:24  unwired-stub
//	hook     useListEstimates              frontends/dashboard/src/hooks/roofops-service-hooks_gen.ts:212
//	deploy-target FirebaseHosting          forge:kcl/schema.k:1851  on=Frontend  required=project,site,public_dir  …

type shape struct {
	Kind   string // rpc | message | enum | table | store | handler | hook | deploy-target
	Name   string
	File   string
	Line   int
	Detail string
}

var (
	reProtoRPC     = regexp.MustCompile(`^\s*rpc\s+(\w+)\s*\(\s*((?:stream\s+)?[\w.]+)\s*\)\s*returns\s*\(\s*((?:stream\s+)?[\w.]+)\s*\)`)
	reProtoMessage = regexp.MustCompile(`^message\s+(\w+)\s*\{`)
	reProtoEnum    = regexp.MustCompile(`^enum\s+(\w+)\s*\{`)
	reProtoService = regexp.MustCompile(`^service\s+(\w+)\s*\{`)
	reSQLTable     = regexp.MustCompile(`(?i)^\s*CREATE\s+TABLE\s+(?:IF\s+NOT\s+EXISTS\s+)?(` + sqlident.Qualified + `)`)
	reGoMethod     = regexp.MustCompile(`^func\s+\(\s*(?:\w+\s+)?\*Service\s*\)\s+(\w+)\s*\(`)
	reUnwiredStub  = regexp.MustCompile(`forge:gen unwired-stub symbol=(\S+)`)
	reTSHook       = regexp.MustCompile(`^export\s+(?:const|function)\s+(use\w+)`)
	reStoreIface   = regexp.MustCompile(`^type\s+(\w*Store)\s+interface\s*\{`)
)

func newShapesCmd() *cobra.Command {
	var (
		grepPat       string
		kinds         string
		service       string
		unimplemented bool
	)
	cmd := &cobra.Command{
		Use:   "shapes",
		Short: "List every API shape — RPCs, messages, enums, tables, handlers, hooks — with file:line",
		Long: `List every API shape in the project with its source location.

Derived LIVE from .proto, db/migrations and source on every call — never from
gen/forge_descriptor.json, which is a cache and can be stale. Answers "where is
X and what shape is it" in ONE call instead of a sequence of greps.

Start recon here. A measured fan-out spent 28 minutes across 123 calls
re-deriving this by hand, mostly paging one large proto by symbol.

--grep is a REGEX, so ask about every entity you own in ONE call. A later run
adopted this command and still invoked it once per entity — 11 turns of
--grep Property then --grep Customer — which is the old grep loop wearing a
new command's name.

Examples:
  forge project shapes                          # everything
  forge project shapes --grep Estimate          # one entity across all layers
  forge project shapes --grep 'Invoice|Payment' # SEVERAL entities in one call
  forge project shapes --kind rpc,handler       # what is declared vs implemented
  forge project shapes --service ChatService    # one service's rpcs and their handlers
  forge project shapes --unimplemented          # declared rpcs with no handler (or a stub)
  forge project shapes --kind deploy-target     # what a workload's deploy= accepts

A handler line is named like the rpc it serves (ChatService.SendMessage), so
rpc and handler lines diff by name. Only methods on a handler package's
*Service that ARE an rpc of its service are handlers: helpers, Name and
Register are not listed.`,
		RunE: func(cmd *cobra.Command, args []string) error {
			projectDir, err := os.Getwd()
			if err != nil {
				return err
			}
			shapes := collectShapes(projectDir)
			if service != "" {
				shapes = shapesOfService(shapes, service)
			}
			if unimplemented {
				return printUnimplemented(cmd.OutOrStdout(), shapes)
			}

			var want map[string]bool
			if kinds != "" {
				want = map[string]bool{}
				for _, k := range strings.Split(kinds, ",") {
					want[strings.TrimSpace(k)] = true
				}
			}
			var pat *regexp.Regexp
			if grepPat != "" {
				pat, err = regexp.Compile("(?i)" + grepPat)
				if err != nil {
					return fmt.Errorf("invalid --grep pattern: %w", err)
				}
			}

			out := cmd.OutOrStdout()

			// Group by the alternation branch that matched, so ONE call asking
			// about several entities reads like several answers instead of one
			// undifferentiated wall. Without this the batched form is harder to
			// read than repeated calls, and a measured run duly made repeated
			// calls — 11 turns of --grep Property then --grep Customer.
			groups := grepBranches(grepPat)

			matched := make([][]shape, len(groups)+1)
			n := 0
			for _, s := range shapes {
				if want != nil && !want[s.Kind] {
					continue
				}
				if pat != nil && !pat.MatchString(s.Name) && !pat.MatchString(s.Detail) {
					continue
				}
				matched[branchIndex(groups, s)] = append(matched[branchIndex(groups, s)], s)
				n++
			}

			write := func(s shape) {
				loc := fmt.Sprintf("%s:%d", s.File, s.Line)
				if s.Detail != "" {
					fmt.Fprintf(out, "%-8s %-42s %-58s %s\n", s.Kind, s.Name, loc, s.Detail)
				} else {
					fmt.Fprintf(out, "%-8s %-42s %s\n", s.Kind, s.Name, loc)
				}
			}
			for i, g := range groups {
				if len(matched[i]) == 0 {
					continue
				}
				if len(groups) > 1 {
					fmt.Fprintf(out, "\n── %s ── %d shape(s)\n", g, len(matched[i]))
				}
				for _, s := range matched[i] {
					write(s)
				}
			}
			// Anything matching the pattern but no single branch (a detail-only
			// hit, or an un-split pattern) still prints — dropping it would make
			// the grouped form lossy, which is worse than ungrouped.
			if rest := matched[len(groups)]; len(rest) > 0 {
				if len(groups) > 1 {
					fmt.Fprintf(out, "\n── other ── %d shape(s)\n", len(rest))
				}
				for _, s := range rest {
					write(s)
				}
			}

			if n == 0 {
				fmt.Fprintln(out, "no shapes matched (is this a forge project with protos?)")
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&grepPat, "grep", "", "case-insensitive regex filter over name and detail")
	cmd.Flags().StringVar(&kinds, "kind", "", "comma-separated kinds: rpc,message,enum,table,store,handler,hook,deploy-target")
	cmd.Flags().StringVar(&service, "service", "", "only this service's rpcs and handlers (ChatService, or Chat)")
	cmd.Flags().BoolVar(&unimplemented, "unimplemented", false,
		"list the declared rpcs of forge-managed services that have no handler method, or only forge's unwired stub")
	return cmd
}

// shapesOfService keeps the rpc and handler shapes of one service. A handler
// is named like the rpc it serves, so both carry the "<Service>." prefix.
func shapesOfService(shapes []shape, service string) []shape {
	prefixes := []string{service + "."}
	if !strings.HasSuffix(service, "Service") {
		prefixes = append(prefixes, service+"Service.")
	}
	var out []shape
	for _, s := range shapes {
		if s.Kind != "rpc" && s.Kind != "handler" {
			continue
		}
		for _, p := range prefixes {
			if strings.HasPrefix(s.Name, p) {
				out = append(out, s)
				break
			}
		}
	}
	return out
}

// printUnimplemented lists each declared rpc of a forge-managed service whose
// service has no handler method for it, or only forge's unwired stub — the
// declared-vs-implemented diff, computed rather than eyeballed.
func printUnimplemented(w io.Writer, shapes []shape) error {
	handlers := map[string]shape{}
	for _, s := range shapes {
		if s.Kind == "handler" {
			handlers[s.Name] = s
		}
	}
	n := 0
	for _, s := range shapes {
		if s.Kind != "rpc" || strings.HasSuffix(s.Detail, " unmanaged") {
			continue
		}
		loc := fmt.Sprintf("%s:%d", s.File, s.Line)
		h, ok := handlers[s.Name]
		switch {
		case !ok:
			fmt.Fprintf(w, "%-42s %-58s no handler method\n", s.Name, loc)
		case strings.Contains(h.Detail, "unwired-stub"):
			fmt.Fprintf(w, "%-42s %-58s unwired stub at %s:%d\n", s.Name, loc, h.File, h.Line)
		default:
			continue
		}
		n++
	}
	if n == 0 {
		fmt.Fprintln(w, "every declared rpc of a forge-managed service has a handler")
		return nil
	}
	fmt.Fprintf(w, "%d declared rpc(s) without a handler\n", n)
	return nil
}

// collectShapes walks the sources. Every scan is a cheap line scan — the whole
// point is that this is fast enough to never need caching.
func collectShapes(projectDir string) []shape {
	var out []shape
	rpcs := scanProtos(filepath.Join(projectDir, "proto"))
	out = append(out, rpcs...)
	out = append(out, scanMigrations(filepath.Join(projectDir, "db", "migrations"))...)
	ormDir := codegen.ProjectORMPackage(projectDir).PackageDir()
	out = append(out, scanStores(filepath.Join(projectDir, filepath.FromSlash(ormDir)), ormDir)...)
	out = append(out, scanHandlers(projectDir, rpcs)...)
	out = append(out, scanHooks(filepath.Join(projectDir, "frontends"))...)
	out = append(out, scanDeployTargets(projectDir)...)

	sort.SliceStable(out, func(i, j int) bool {
		if out[i].Kind != out[j].Kind {
			return kindRank(out[i].Kind) < kindRank(out[j].Kind)
		}
		return out[i].Name < out[j].Name
	})
	return out
}

func kindRank(k string) int {
	switch k {
	case "rpc":
		return 0
	case "handler":
		return 1
	case "message":
		return 2
	case "enum":
		return 3
	case "table":
		return 4
	case "store":
		return 5
	case "hook":
		return 6
	case "deploy-target":
		return 7
	}
	return 8
}

// eachLine runs fn over every line of every file under root matching ext.
// Errors are skipped rather than reported: a shapes listing that fails because
// one file is unreadable is less useful than a partial one.
func eachLine(root, ext string, fn func(rel string, lineNo int, line string)) {
	_ = filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return nil //nolint:nilerr // partial output beats no output
		}
		if ext != "" && !strings.HasSuffix(path, ext) {
			return nil
		}
		if strings.Contains(path, "node_modules") || strings.Contains(path, "/.next/") {
			return filepath.SkipDir
		}
		f, openErr := os.Open(path)
		if openErr != nil {
			return nil //nolint:nilerr // same
		}
		defer func() { _ = f.Close() }()

		rel := path
		if wd, wdErr := os.Getwd(); wdErr == nil {
			if r, relErr := filepath.Rel(wd, path); relErr == nil {
				rel = r
			}
		}
		sc := bufio.NewScanner(f)
		sc.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)
		for i := 1; sc.Scan(); i++ {
			fn(rel, i, sc.Text())
		}
		return nil
	})
}

func scanProtos(root string) []shape {
	var out []shape
	var (
		file string
		svc  string
		// An rpc of a `// forge:unmanaged` service is declared but has no
		// forge handler by design; its detail says so, so `--kind
		// rpc,handler` does not read it as an unimplemented rpc.
		svcUnmanaged, pendingUnmanaged bool
	)
	eachLine(root, ".proto", func(rel string, n int, line string) {
		if rel != file {
			file, svc, svcUnmanaged, pendingUnmanaged = rel, "", false, false
		}
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "//") {
			if codegen.IsUnmanagedMarkerLine(trimmed) {
				pendingUnmanaged = true
			}
			return
		}
		if m := reProtoService.FindStringSubmatch(line); m != nil {
			svc, svcUnmanaged, pendingUnmanaged = m[1], pendingUnmanaged, false
			return
		}
		if trimmed != "" {
			pendingUnmanaged = false
		}
		if m := reProtoRPC.FindStringSubmatch(line); m != nil {
			name := m[1]
			if svc != "" {
				name = svc + "." + m[1]
			}
			detail := fmt.Sprintf("in=%s out=%s", m[2], m[3])
			if svcUnmanaged {
				detail += " unmanaged"
			}
			out = append(out, shape{"rpc", name, rel, n, detail})
			return
		}
		if m := reProtoMessage.FindStringSubmatch(line); m != nil {
			out = append(out, shape{"message", m[1], rel, n, ""})
			return
		}
		if m := reProtoEnum.FindStringSubmatch(line); m != nil {
			out = append(out, shape{"enum", m[1], rel, n, ""})
		}
	})
	return out
}

func scanMigrations(root string) []shape {
	var out []shape
	eachLine(root, ".up.sql", func(rel string, n int, line string) {
		if m := reSQLTable.FindStringSubmatch(line); m != nil {
			// pg_dump qualifies every table (`public.tasks`); the table is
			// the last part, and a schema is kept as detail rather than
			// read as the name.
			parts := sqlident.Parts(m[1])
			detail := ""
			if len(parts) > 1 {
				detail = "schema=" + parts[len(parts)-2]
			}
			out = append(out, shape{"table", parts[len(parts)-1], rel, n, detail})
		}
	})
	return out
}

// scanHandlers reports the *Service methods that ARE the RPC handlers, and
// flags the ones forge stamped as unimplemented. The stub marker is the same
// one the fan-out work list is derived from, so "declared but not implemented"
// is answerable from this output alone.
//
// A handler is named like the rpc it serves — <Service>.<Rpc> — so the two
// kinds diff by name and filter by service. The handler package's service is
// resolved the way generate resolves it (codegen.ResolveServiceComponent).
// Only methods that ARE an rpc of that service are handlers: an unexported
// helper, Name and Register sit on *Service too, and listing them as handlers
// made "declared vs implemented" a diff no one could compute. A package no
// declared service resolves to keeps its exported methods under their bare
// names, detail "no declared service", so nothing vanishes silently.
func scanHandlers(projectDir string, rpcs []shape) []shape {
	declared := map[string]map[string]bool{} // service → its rpc names
	for _, r := range rpcs {
		if svc, rpc, ok := strings.Cut(r.Name, "."); ok && r.Kind == "rpc" {
			if declared[svc] == nil {
				declared[svc] = map[string]bool{}
			}
			declared[svc][rpc] = true
		}
	}
	dirService := map[string]string{} // absolute handler dir → service
	for svc := range declared {
		res, err := codegen.ResolveServiceComponent(projectDir, svc)
		if err != nil || !res.FromDisk {
			continue
		}
		if abs, aerr := filepath.Abs(res.Dir); aerr == nil {
			if _, taken := dirService[abs]; !taken {
				dirService[abs] = svc
			}
		}
	}

	var out []shape
	pending := ""
	eachLine(filepath.Join(projectDir, "internal", "handlers"), ".go", func(rel string, n int, line string) {
		if strings.HasSuffix(rel, "_test.go") {
			return
		}
		if m := reUnwiredStub.FindStringSubmatch(line); m != nil {
			pending = m[1]
			return
		}
		m := reGoMethod.FindStringSubmatch(line)
		if m == nil {
			return
		}
		detail := ""
		if pending != "" {
			detail = "unwired-stub"
			pending = ""
		}
		dir, _ := filepath.Abs(filepath.Dir(rel))
		svc, known := dirService[dir]
		switch {
		case known && declared[svc][m[1]]:
			out = append(out, shape{"handler", svc + "." + m[1], rel, n, detail})
		case known:
			// A helper, Name or Register on the service: not a handler.
		case ast.IsExported(m[1]):
			out = append(out, shape{"handler", m[1], rel, n, strings.TrimSpace(detail + " no declared service")})
		}
	})
	return out
}

// scanStores reports the generated persistence interfaces in internal/db —
// one `<Entity>Store` per entity plus the aggregate `Store` — which are
// what a service names as a Deps field.
//
// It is here because the alternative was measured: a run spent 11 turns
// reading forge's OWN generator source (internal/generator/plan_orm_store_gen.go,
// `git log` included) to work out what the generated store offers. That is
// the worst possible source for the answer — it is the code that WRITES the
// file, in a different repo, describing every project rather than this one.
// The generated file itself is right there and is specific to this schema.
//
// The detail column carries the file the reader should open, because the
// name alone still leaves "and what are its methods" unanswered; `go doc`
// does render an interface's method set in full, so unlike a struct it is a
// route that actually terminates.
func scanStores(root, ormDir string) []shape {
	var out []shape
	eachLine(root, ".go", func(rel string, n int, line string) {
		if strings.HasSuffix(rel, "_test.go") {
			return
		}
		if m := reStoreIface.FindStringSubmatch(line); m != nil {
			out = append(out, shape{"store", m[1], rel, n, "go doc ./" + ormDir + " " + m[1]})
		}
	})
	return out
}

// scanDeployTargets reports what a workload's `deploy` field may be SET TO —
// the schemas behind `deploy = forge.FirebaseHosting { … }`.
//
// It is here because this is the one capability with no generated file to
// live in. Scaffold a frontend and exactly one `forge.Frontend` block is
// emitted, in dev/main.k; staging and prod declare no frontend workload at
// all, so a user reading prod config to work out how to ship their frontend
// finds nothing, and `forge env deploy prod` quietly ships the Go services
// and not the app. Meanwhile schema.k has supported
// `Frontend.deploy?: FirebaseHosting | K8sCluster` all along, with a good
// docstring. The capability was built and then made invisible.
//
// `forge project capabilities` does not close this: it lists VERBS, and what
// is missing here is a SCHEMA — "what can a Frontend *be*".
//
// The set is reflected out of the embedded kcl module (kcl.DeployTargets),
// not listed here. See kcl/targets.go for why that is the whole point: a
// literal list in this file would be a second source of truth that rots on
// the next commit adding a target.
//
// Unlike every other scanner this one does not read projectDir — the schemas
// ride inside the binary, so it answers in a non-project directory too. The
// parameter is kept for signature symmetry with the rest of collectShapes.
// kclModuleRefPrefix labels a location inside forge's own KCL module
// (github.com/reliant-labs/forge/kcl), which the binary supplies at render
// time rather than the project carrying a copy.
const kclModuleRefPrefix = "forge:kcl/"

func scanDeployTargets(_ string) []shape {
	targets, err := kcl.DeployTargets()
	if err != nil {
		return nil //nolint:nilerr // partial output beats no output
	}
	out := make([]shape, 0, len(targets))
	for _, t := range targets {
		// Which workload kind accepts this target is load-bearing and is
		// part of what users are missing: Service-side and Frontend-side
		// unions differ, so "K8sCluster" alone does not tell you whether
		// you may write it on a frontend. It leads the detail column.
		detail := "on=" + strings.Join(t.Workloads, ",")
		if req := requiredFieldNames(t.Fields); len(req) > 0 {
			detail += "  required=" + strings.Join(req, ",")
		}
		if opt := optionalFieldNames(t.Fields); len(opt) > 0 {
			detail += "  optional=" + strings.Join(opt, ",")
		}
		if t.Doc != "" {
			detail += "  " + t.Doc
		}
		file := t.File
		if file != "" {
			// The schema lives in forge's KCL module, which the binary
			// supplies at render time — it is not a file in the project.
			// Name it as a path inside that module (github.com/
			// reliant-labs/forge/kcl/<file>), with a prefix no project path
			// can have, so nobody greps their tree for it.
			file = kclModuleRefPrefix + file
		}
		out = append(out, shape{"deploy-target", t.Name, file, t.Line, detail})
	}
	return out
}

// requiredFieldNames returns the fields that MUST be supplied — the ones a
// user has to write to make the block valid. Separating them from the
// optional set is the difference between "here is a wall of 15 fields" and
// "write these three".
func requiredFieldNames(fields []kcl.SchemaField) []string {
	var out []string
	for _, f := range fields {
		// `type` is the union discriminator and is always defaulted by the
		// schema; it is never something a user writes.
		if f.Name == "type" {
			continue
		}
		if f.Required() {
			out = append(out, f.Name)
		}
	}
	return out
}

func optionalFieldNames(fields []kcl.SchemaField) []string {
	var out []string
	for _, f := range fields {
		if f.Name == "type" || f.Required() {
			continue
		}
		out = append(out, f.Name)
	}
	return out
}

func scanHooks(root string) []shape {
	var out []shape
	eachLine(root, "-hooks_gen.ts", func(rel string, n int, line string) {
		if m := reTSHook.FindStringSubmatch(line); m != nil {
			out = append(out, shape{"hook", m[1], rel, n, ""})
		}
	})
	return out
}

// grepBranches splits a --grep pattern on TOP-LEVEL alternation so a batched
// query can be reported per entity. `Invoice|Payment` yields two branches;
// anything carrying regex metacharacters beyond a plain alternation yields one
// branch (the whole pattern), because splitting those would mis-attribute.
//
// Grouping exists to make the batched call the EASIER one to read. The command
// already accepted a regex, and a measured run still asked one entity per turn
// — a flat merged list gives the caller no reason to prefer the single call.
func grepBranches(pattern string) []string {
	if pattern == "" || !strings.Contains(pattern, "|") {
		if pattern == "" {
			return nil
		}
		return []string{pattern}
	}
	parts := strings.Split(pattern, "|")
	for _, p := range parts {
		p = strings.TrimSpace(p)
		// A branch that is not a bare word means the pattern is doing something
		// structural; report it whole rather than guessing at its parts.
		if p == "" || strings.ContainsAny(p, "()[]{}.*+?^$\\") {
			return []string{pattern}
		}
	}
	out := make([]string, 0, len(parts))
	for _, p := range parts {
		out = append(out, strings.TrimSpace(p))
	}
	return out
}

// branchIndex returns the index of the first branch matching this shape, or
// len(branches) when none does (detail-only hits, or a single unsplit pattern).
func branchIndex(branches []string, s shape) int {
	for i, b := range branches {
		if strings.Contains(strings.ToLower(s.Name), strings.ToLower(b)) {
			return i
		}
	}
	return len(branches)
}
