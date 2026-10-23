package contract

// The exclusion gate — what `//forge:exclude-contract` costs.
//
// # Why this exists
//
// `//forge:exclude-contract` opts a package out of every contract rule, the
// injector and mock generation at once. That made it the cheapest way to turn
// `forge lint` green: one comment line, no reason, no review. Agents learned
// that, and stamped it on packages that were exactly what the contract system
// is for — an Authority with three interchangeable stores, an HTTP client to
// another service, a k8s Secret provisioner. Each one lost its mock, its
// decorator and its seam, and nothing said so.
//
// So the marker now costs two things.
//
//  1. A REASON. `//forge:exclude-contract: <why>`. A bare marker still excludes
//     (codegen never breaks over an opt-out's spelling) but lint refuses it —
//     the same bargain `forge:lint-disable` strikes for error-severity rules
//     (internal/linter/suppress): a reason turns a shrug into a decision a
//     reviewer can evaluate.
//
//  2. NOT BEING WRONG. The marker is refused outright on a package that does
//     OUTBOUND I/O or that owns a multi-implementation interface — the two
//     shapes the contracts and adapter skills name as needing a contract no
//     matter what. A reason cannot talk its way past those; the fix is a
//     contract.go (the adapter shape for I/O).
//
// # Why a module pass, not a per-package analyzer
//
// "Two implementations" is a fact about the MODULE: the second implementation
// of tokenauthority.Authority lives in accesstokenclient. A go/analysis pass
// sees one package; the in-process lint driver already holds every loaded
// package with full type information, so the check runs there once, over all
// of them.
//
// # The signals, and why they are low-noise
//
// I/O is a DIRECT CALL (resolved by the type checker, not by name) whose
// callee belongs to an outbound-I/O entry point: a method on *net/http.Client,
// the http.Get family, database/sql's handles, pgx, a controller-runtime or
// client-go client, a gRPC/Connect client, NATS, an OCI registry client, a
// cloud SDK client. Importing a package is not enough — a types-only package
// that names `client.Object` in a signature does no I/O. Local file I/O
// (`os`) is deliberately NOT a signal: a file-format library over a
// caller-supplied path is a legitimate pure-ish library. Nor is a dial or
// HTTP call whose target is STATICALLY loopback (a constant 127.0.0.1 / ::1
// / localhost address, or net.JoinHostPort over such a host): it cannot
// leave the machine, so it is not a third-party boundary. A target the type
// checker cannot fold to a constant is still I/O, and the refusal's hint
// says how to make a genuinely-loopback one visible.
//
// Multi-implementation is an EXPORTED interface declared in the package with
// at least two distinct non-test, non-generated named types in the module
// implementing it, at least one of them IN the package itself, that the
// package does not itself CONSUME. Both conditions keep the house rule —
// declare interfaces at the consumer, WET over DRY — from tripping it:
//
//   - a consumer-side seam implemented only elsewhere (reconcile.Observer) has
//     no in-package implementation;
//   - a consumer-side seam the package also ships a default for is still a
//     seam. reliant's accountlimits declares `TokenSource` for its Fetcher's
//     `Tokens` field, with an in-package `StoredTokens` and the real one in
//     llm/drivers. Two implementations and one of them in-package, so the
//     gate refused the package's exclusion and prescribed a contract.go — for
//     an interface the package NEEDS, not one it offers. consumesInterface
//     tells the two apart: the package holds the interface in a struct field
//     or takes it as a parameter, on something that is neither an
//     implementation of it (a decorator's `next`) nor a producer of it (a
//     constructor or combinator returning it). That is the same line the
//     analyzer draws for Deps seams ("a requirement, not an offer").
//
// A strategy registry (an exported `Register*` func taking the interface) is
// the one multi-impl shape the contracts skill names as legitimately
// excluded, and is exempt.
//
// # Exemptions
//
// Test-support packages (their non-test source imports "testing") exist to
// stand up fakes; refusing them for doing so would be noise. Packages whose
// component kind forge already exempts structurally from the contract rules
// (the app composition seam, supervised workers, reconciler/operator
// packages) are shaped by forge's own runtime interfaces, not by a Service.
//
// A genuinely-justified exception uses the ordinary suppression mechanism on
// the marker's line — `// forge:lint-disable-next-line <rule>: <why>` — so it
// is visible, reasoned, and reported by `--show-suppressed` like every other.

import (
	"fmt"
	"go/ast"
	"go/constant"
	"go/token"
	"go/types"
	"net"
	"net/url"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"golang.org/x/tools/go/packages"
	"golang.org/x/tools/go/types/typeutil"

	"github.com/reliant-labs/forge/internal/codegen"
	"github.com/reliant-labs/forge/internal/linter/finding"
	"github.com/reliant-labs/forge/internal/linter/suppress"
)

// Rule IDs of the exclusion gate. Stable: they appear in findings and are the
// names a `forge:lint-disable` directive suppresses.
const (
	// RuleExcludeReasonRequired refuses a bare `//forge:exclude-contract`.
	RuleExcludeReasonRequired = "forge-exclude-contract-reason"
	// RuleExcludeOutboundIO refuses the marker on a package doing outbound I/O.
	RuleExcludeOutboundIO = "forge-exclude-contract-outbound-io"
	// RuleExcludeMultiImpl refuses the marker on a package owning a
	// multi-implementation interface.
	RuleExcludeMultiImpl = "forge-exclude-contract-multi-impl"
)

// outboundIOSignal is one family of outbound-I/O entry points. A call whose
// callee's package path matches pkgPath (exactly, or as a prefix when
// prefix is set) and — when recv is non-empty — whose receiver type name is
// one of recv, marks the calling package as doing outbound I/O.
type outboundIOSignal struct {
	label   string
	pkgPath string
	prefix  bool
	// recv restricts to methods on these named receiver types. Empty means
	// any function or method in the package.
	recv []string
	// funcs restricts package-level functions to these names. Only consulted
	// when recv is empty.
	funcs []string
}

// outboundIOSignals is the catalogue. Each entry is an entry point that
// cannot be reached without leaving the process — never a type, an
// interface, or a helper that merely lives in the same package.
func outboundIOSignals() []outboundIOSignal {
	return []outboundIOSignal{
		{label: "an HTTP client call (net/http)", pkgPath: "net/http", recv: []string{"Client", "Transport"}},
		{label: "an HTTP client call (net/http)", pkgPath: "net/http", funcs: []string{"Get", "Head", "Post", "PostForm"}},
		{label: "a network dial (net)", pkgPath: "net", funcs: []string{"Dial", "DialTimeout", "DialTCP", "DialUDP", "DialUnix"}},
		{label: "a network dial (net)", pkgPath: "net", recv: []string{"Dialer"}},
		{label: "a database call (database/sql)", pkgPath: "database/sql", recv: []string{"DB", "Tx", "Conn", "Stmt"}},
		{label: "a database call (pgx)", pkgPath: "github.com/jackc/pgx", prefix: true},
		{label: "a Kubernetes API call (controller-runtime client)", pkgPath: "sigs.k8s.io/controller-runtime/pkg/client", recv: []string{"Client", "Reader", "Writer", "StatusWriter", "SubResourceClient", "SubResourceWriter"}},
		{label: "a Kubernetes API call (client-go)", pkgPath: "k8s.io/client-go/kubernetes", prefix: true},
		{label: "a Kubernetes API call (client-go)", pkgPath: "k8s.io/client-go/dynamic", prefix: true},
		{label: "a gRPC client call", pkgPath: "google.golang.org/grpc", recv: []string{"ClientConn"}},
		{label: "a gRPC client dial", pkgPath: "google.golang.org/grpc", funcs: []string{"Dial", "DialContext", "NewClient"}},
		{label: "a Connect client call", pkgPath: "connectrpc.com/connect", recv: []string{"Client"}},
		{label: "a NATS call", pkgPath: "github.com/nats-io/nats.go", prefix: true, recv: []string{"Conn", "EncodedConn", "JetStreamContext", "Subscription", "KeyValue", "ObjectStore"}},
		{label: "a NATS dial", pkgPath: "github.com/nats-io/nats.go", funcs: []string{"Connect"}},
		{label: "a NATS JetStream call", pkgPath: "github.com/nats-io/nats.go/jetstream", prefix: true},
		{label: "a Redis call", pkgPath: "github.com/redis/go-redis", prefix: true},
		{label: "an OCI registry call (go-containerregistry remote)", pkgPath: "github.com/google/go-containerregistry/pkg/v1/remote", prefix: true},
		{label: "an OCI registry call (oras)", pkgPath: "oras.land/oras-go", prefix: true},
		{label: "a cloud SDK call (AWS)", pkgPath: "github.com/aws/aws-sdk-go-v2/service", prefix: true},
		{label: "a cloud SDK call (GCP)", pkgPath: "cloud.google.com/go", prefix: true},
		{label: "a cloud SDK call (Azure)", pkgPath: "github.com/Azure/azure-sdk-for-go", prefix: true},
		{label: "a Stripe API call", pkgPath: "github.com/stripe/stripe-go", prefix: true},
		{label: "an S3-compatible call (minio)", pkgPath: "github.com/minio/minio-go", prefix: true},
	}
}

// ioSignalFor reports the label of the outbound-I/O family fn belongs to.
func ioSignalFor(fn *types.Func) (string, bool) {
	if fn == nil || fn.Pkg() == nil {
		return "", false
	}
	path := fn.Pkg().Path()
	recvName := ""
	if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil {
		recvName = namedTypeName(sig.Recv().Type())
	}
	for _, s := range outboundIOSignals() {
		if s.prefix {
			if path != s.pkgPath && !strings.HasPrefix(path, s.pkgPath+"/") {
				continue
			}
		} else if path != s.pkgPath {
			continue
		}
		switch {
		case len(s.recv) > 0:
			if recvName != "" && contains(s.recv, recvName) {
				return s.label, true
			}
		case len(s.funcs) > 0:
			if recvName == "" && contains(s.funcs, fn.Name()) {
				return s.label, true
			}
		default:
			return s.label, true
		}
	}
	return "", false
}

// namedTypeName returns the name of t's underlying named type (through one
// pointer), or "" for an unnamed type. An interface method's receiver is the
// interface itself, so `client.Client.Get` resolves to "Client".
func namedTypeName(t types.Type) string {
	if p, ok := t.(*types.Pointer); ok {
		t = p.Elem()
	}
	switch n := t.(type) {
	case *types.Named:
		return n.Obj().Name()
	case *types.Alias:
		return namedTypeName(types.Unalias(n))
	}
	return ""
}

func contains(list []string, s string) bool {
	for _, v := range list {
		if v == s {
			return true
		}
	}
	return false
}

// ExcludeFinding is one refusal of an exclude-contract marker.
type ExcludeFinding struct {
	Rule     string
	Severity finding.Severity
	File     string // absolute
	Line     int
	Message  string
	FixHint  string
}

// ExcludeGateOptions tunes the gate for the tree it runs over.
type ExcludeGateOptions struct {
	// CodegenUnavailable reports that `forge generate` does not run here
	// (no forge.yaml). The outbound-io and multi-impl refusals exist to
	// deliver "a contract's mock and decorator", which are forge generate
	// output; with no codegen that payoff does not exist, so the refusals
	// report as WARNINGS that say so. A missing reason stays an error: it
	// costs one line and is worth it in any repository.
	CodegenUnavailable bool
}

// codegenUnavailableNote is appended to a refusal downgraded under
// ExcludeGateOptions.CodegenUnavailable.
const codegenUnavailableNote = " (Reported as a warning: this tree has no forge.yaml, so a contract.go gets no " +
	"generated mock or decorator here. The boundary is still worth a narrow interface; `forge lint --strict` gates it.)"

// CheckExcludeDirectives applies the exclusion gate to every root package in
// pkgs (the packages `forge lint` was asked about, loaded with types). The
// multi-implementation search spans every loaded package, so callers should
// load the whole module (`./...`) for an accurate answer; a narrower load can
// only miss an implementation, never invent one.
func CheckExcludeDirectives(pkgs []*packages.Package, opts ExcludeGateOptions) []ExcludeFinding {
	// De-duplicate test variants: `Tests: true` loads `p`, `p [p.test]` and
	// `p_test`; the gate is about the package's own non-test source, which
	// the plain variant carries.
	roots := map[string]*packages.Package{}
	for _, p := range pkgs {
		if p.Types == nil || strings.HasSuffix(p.PkgPath, "_test") || strings.HasSuffix(p.PkgPath, ".test") {
			continue
		}
		if p.ID != p.PkgPath {
			// A test variant (`p [p.test]`); prefer the plain package.
			if _, ok := roots[p.PkgPath]; ok {
				continue
			}
		}
		roots[p.PkgPath] = p
	}

	// Every named type in the MAIN module's non-test, non-generated source,
	// for the implementations search. Test variants (`p [p.test]`) re-declare
	// the same types under a second types.Package; keying on path+name counts
	// each declaration once.
	var candidates []*types.TypeName
	seen := map[string]bool{}
	packages.Visit(pkgs, nil, func(p *packages.Package) {
		if p.Types == nil || p.TypesInfo == nil || p.Module == nil || !p.Module.Main || p.ID != p.PkgPath {
			return
		}
		for ident, obj := range p.TypesInfo.Defs {
			tn, ok := obj.(*types.TypeName)
			if !ok || tn.IsAlias() || tn.Parent() != tn.Pkg().Scope() {
				continue
			}
			key := tn.Pkg().Path() + "." + tn.Name()
			if seen[key] {
				continue
			}
			file := p.Fset.Position(ident.Pos()).Filename
			if isNonSourceFile(file) {
				continue
			}
			if _, isIface := tn.Type().Underlying().(*types.Interface); isIface {
				continue
			}
			seen[key] = true
			candidates = append(candidates, tn)
		}
	})

	paths := make([]string, 0, len(roots))
	for path := range roots {
		paths = append(paths, path)
	}
	sort.Strings(paths)

	var out []ExcludeFinding
	for _, path := range paths {
		p := roots[path]
		dir := pkgDir(p)
		if dir == "" {
			continue
		}
		markers := codegen.FindExcludeContractMarkers(dir)
		if len(markers) == 0 {
			continue
		}
		marker, conflict := packageDeclaration(p.PkgPath, "forge:exclude-contract", markers,
			[]string{RuleExcludeReasonRequired, RuleExcludeOutboundIO, RuleExcludeMultiImpl}, RuleExcludeConflict)
		if conflict != nil {
			out = append(out, *conflict)
			continue
		}
		out = append(out, checkOnePackage(p, marker, candidates)...)
	}
	// Grade BEFORE suppressions apply: a warning needs no reason to be
	// silenced, so a reasonless allowance of a downgraded refusal must not
	// come back as a missing-reason error.
	if opts.CodegenUnavailable {
		for i := range out {
			if out[i].Rule == RuleExcludeOutboundIO || out[i].Rule == RuleExcludeMultiImpl {
				out[i].Severity = finding.SeverityWarning
				out[i].FixHint += codegenUnavailableNote
			}
		}
	}
	return applyDirectiveSuppressions(out)
}

func checkOnePackage(p *packages.Package, marker codegen.ReasonedMarker, candidates []*types.TypeName) []ExcludeFinding {
	var out []ExcludeFinding
	if marker.Reason == "" {
		out = append(out, ExcludeFinding{
			Rule:     RuleExcludeReasonRequired,
			Severity: finding.SeverityError,
			File:     marker.File,
			Line:     marker.Line,
			Message: fmt.Sprintf("package %s is excluded from the contract rules with a bare `//forge:exclude-contract` — "+
				"the marker requires a reason", p.PkgPath),
			FixHint: "write why forge should not manage this package: `//forge:exclude-contract: <why>`, once, in the " +
				"package's doc.go (pure functions, test-only support, API/CRD types, a strategy registry). If none of those is true, " +
				"the package wants a contract.go instead — see `forge skill load contracts/package-markers`",
		})
	}
	if isTestSupportPackage(p) || isStructurallyExemptKind(p) {
		return out
	}
	if hit, ok := firstOutboundIOCall(p); ok {
		hint := "remove `//forge:exclude-contract` and make this an adapter: a contract.go carrying " +
			"`// forge:outbound-io` with a narrow Service interface in domain types, and `// forge:constructor` " +
			"on `func New(Deps)` — see `forge skill load adapter`"
		if hit.dynamicTarget {
			hint += ". If this call only ever reaches LOOPBACK (a local dev server, a sidecar on 127.0.0.1), " +
				"it is not an outbound boundary: write the host as a constant — `net.JoinHostPort(\"127.0.0.1\", port)`, " +
				"`\"http://localhost:8080/…\"` — and the gate exempts it, because it can then see the target"
		}
		out = append(out, ExcludeFinding{
			Rule:     RuleExcludeOutboundIO,
			Severity: finding.SeverityError,
			File:     marker.File,
			Line:     marker.Line,
			Message: fmt.Sprintf("package %s does outbound I/O (%s at %s) and so cannot opt out of the contract "+
				"system: an outbound boundary is exactly what a contract's mock and decorator are for",
				p.PkgPath, hit.label, shortPos(hit.pos)),
			FixHint: hint,
		})
	}
	if iface, impls, ok := multiImplInterface(p, candidates); ok {
		out = append(out, ExcludeFinding{
			Rule:     RuleExcludeMultiImpl,
			Severity: finding.SeverityError,
			File:     marker.File,
			Line:     marker.Line,
			Message: fmt.Sprintf("package %s declares interface %s with %d implementations in the module (%s) and so "+
				"cannot opt out of the contract system: an interface with interchangeable implementations IS the contract",
				p.PkgPath, iface, len(impls), strings.Join(impls, ", ")),
			FixHint: fmt.Sprintf("remove `//forge:exclude-contract` and move %s into a contract.go (mark it "+
				"`//forge:contract` if it is not named Service) — see `forge skill load contracts/package-markers`. A strategy "+
				"registry is exempt when it exposes a `Register…(%s)` func", iface, iface),
		})
	}
	return out
}

// outboundHit is one outbound-I/O call found in a package.
type outboundHit struct {
	label string
	pos   token.Position
	// dynamicTarget is set when the callee takes an address or URL (a dial,
	// an http.Get) and this call's target is not a compile-time constant,
	// so the gate cannot tell whether it is loopback.
	dynamicTarget bool
}

// firstOutboundIOCall returns the first (by position) call in p's non-test,
// non-generated source whose callee is an outbound-I/O entry point. A call
// whose target is statically a loopback address is not one (see
// staticLoopbackTarget), and is skipped.
func firstOutboundIOCall(p *packages.Package) (outboundHit, bool) {
	var hits []outboundHit
	for _, f := range p.Syntax {
		file := p.Fset.Position(f.Pos()).Filename
		if isNonSourceFile(file) {
			continue
		}
		ast.Inspect(f, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok {
				return true
			}
			fn, ok := typeutil.Callee(p.TypesInfo, call).(*types.Func)
			if !ok {
				return true
			}
			label, ok := ioSignalFor(fn)
			if !ok {
				return true
			}
			hit := outboundHit{label: label, pos: p.Fset.Position(call.Pos())}
			if idx, takesTarget := targetArgIndex(fn); takesTarget {
				if idx < len(call.Args) && staticLoopbackTarget(p.TypesInfo, call.Args[idx]) {
					return true
				}
				hit.dynamicTarget = true
			}
			hits = append(hits, hit)
			return true
		})
	}
	if len(hits) == 0 {
		return outboundHit{}, false
	}
	sort.Slice(hits, func(i, j int) bool {
		if hits[i].pos.Filename != hits[j].pos.Filename {
			return hits[i].pos.Filename < hits[j].pos.Filename
		}
		return hits[i].pos.Offset < hits[j].pos.Offset
	})
	return hits[0], true
}

// targetArgIndex reports which argument of fn names the remote end, for the
// outbound-I/O entry points whose target is a string: the address of a
// net dial, the URL of a net/http convenience call. Entry points whose
// target is a struct (DialTCP's *TCPAddr, Client.Do's *Request) have none,
// and are judged as outbound whatever they reach.
func targetArgIndex(fn *types.Func) (int, bool) {
	if fn.Pkg() == nil {
		return 0, false
	}
	recv := ""
	if sig, ok := fn.Type().(*types.Signature); ok && sig.Recv() != nil {
		recv = namedTypeName(sig.Recv().Type())
	}
	switch fn.Pkg().Path() {
	case "net":
		switch {
		case recv == "" && (fn.Name() == "Dial" || fn.Name() == "DialTimeout"):
			return 1, true
		case recv == "Dialer" && fn.Name() == "Dial":
			return 1, true
		case recv == "Dialer" && fn.Name() == "DialContext":
			return 2, true
		}
	case "net/http":
		if recv == "" || recv == "Client" {
			switch fn.Name() {
			case "Get", "Head", "Post", "PostForm":
				return 0, true
			}
		}
	}
	return 0, false
}

// staticLoopbackTarget reports whether expr is, at compile time, an address
// or URL on the loopback interface: a constant "127.0.0.1:8080",
// "[::1]:80", "localhost:3000" or "http://localhost:8080/x", or a
// net.JoinHostPort call whose HOST is such a constant (the port may be
// anything). A dial to the machine's own loopback cannot leave it, so it
// is not the third-party boundary a contract's mock and decorator exist
// for. Anything the type checker cannot fold to a constant is NOT loopback:
// the gate only exempts what it can see.
func staticLoopbackTarget(info *types.Info, expr ast.Expr) bool {
	if s, ok := constantString(info, expr); ok {
		return isLoopbackAddress(s)
	}
	call, ok := ast.Unparen(expr).(*ast.CallExpr)
	if !ok || len(call.Args) != 2 {
		return false
	}
	fn, ok := typeutil.Callee(info, call).(*types.Func)
	if !ok || fn.Pkg() == nil || fn.Pkg().Path() != "net" || fn.Name() != "JoinHostPort" {
		return false
	}
	host, ok := constantString(info, call.Args[0])
	return ok && isLoopbackHost(host)
}

// constantString returns expr's value when the type checker folded it to a
// string constant (a literal, a const, a constant expression).
func constantString(info *types.Info, expr ast.Expr) (string, bool) {
	tv, ok := info.Types[expr]
	if !ok || tv.Value == nil || tv.Value.Kind() != constant.String {
		return "", false
	}
	return constant.StringVal(tv.Value), true
}

// isLoopbackAddress reports whether s, a dial address ("host:port") or a
// URL, names a loopback host.
func isLoopbackAddress(s string) bool {
	if strings.Contains(s, "://") {
		u, err := url.Parse(s)
		return err == nil && isLoopbackHost(u.Hostname())
	}
	host, _, err := net.SplitHostPort(s)
	return err == nil && isLoopbackHost(host)
}

// isLoopbackHost reports whether host is a loopback IP (127.0.0.0/8, ::1)
// or the name localhost, which RFC 6761 reserves for loopback.
func isLoopbackHost(host string) bool {
	if strings.EqualFold(host, "localhost") {
		return true
	}
	ip := net.ParseIP(strings.Trim(host, "[]"))
	return ip != nil && ip.IsLoopback()
}

// multiImplInterface returns the first exported, non-empty interface declared
// in p (in non-test, non-generated source) that has >= 2 implementations in
// the module, at least one of them declared in p. A strategy registry — an
// exported `Register*` func accepting the interface — is exempt, and so is an
// interface p itself consumes (consumesInterface): a seam p needs is not a
// contract p offers, whatever implements it.
func multiImplInterface(p *packages.Package, candidates []*types.TypeName) (string, []string, bool) {
	scope := p.Types.Scope()
	names := scope.Names() // sorted
	for _, name := range names {
		tn, ok := scope.Lookup(name).(*types.TypeName)
		if !ok || !tn.Exported() || tn.IsAlias() {
			continue
		}
		iface, ok := tn.Type().Underlying().(*types.Interface)
		if !ok || iface.NumMethods() == 0 {
			continue
		}
		if isNonSourceFile(p.Fset.Position(tn.Pos()).Filename) {
			continue
		}
		if hasRegistryFunc(p, tn.Type()) || consumesInterface(p, tn.Type(), iface) {
			continue
		}
		var impls []string
		inPkg := false
		for _, c := range candidates {
			t := c.Type()
			if !types.Implements(t, iface) && !types.Implements(types.NewPointer(t), iface) {
				continue
			}
			if c.Pkg() == tn.Pkg() || c.Pkg().Path() == tn.Pkg().Path() {
				inPkg = true
				impls = append(impls, c.Name())
			} else {
				impls = append(impls, c.Pkg().Name()+"."+c.Name())
			}
		}
		if len(impls) >= 2 && inPkg {
			sort.Strings(impls)
			return name, impls, true
		}
	}
	return "", nil, false
}

// consumesInterface reports whether p's non-test, non-generated source takes
// t (interface iface) as a dependency: as the type of a struct field, or of a
// function or method parameter — directly or as the element of a pointer,
// slice, array, map or channel. Two declarations are not consumers, because
// they are the provider's own shapes:
//
//   - a struct, or a method on a type, that implements t: a decorator's
//     `next Store` or a fan-out over `[]Store` IS a Store;
//   - a function or method returning t: a constructor or combinator
//     (`func Wrap(s Store) Store`) produces the contract rather than needing
//     it.
func consumesInterface(p *packages.Package, t types.Type, iface *types.Interface) bool {
	implements := func(typ types.Type) bool {
		if typ == nil {
			return false
		}
		if ptr, ok := typ.(*types.Pointer); ok {
			typ = ptr.Elem()
		}
		if _, isIface := typ.Underlying().(*types.Interface); isIface {
			return false
		}
		return types.Implements(typ, iface) || types.Implements(types.NewPointer(typ), iface)
	}
	takes := func(fields *ast.FieldList) bool {
		if fields == nil {
			return false
		}
		for _, f := range fields.List {
			expr := f.Type
			if ell, ok := expr.(*ast.Ellipsis); ok {
				expr = ell.Elt // a variadic `...T` records no type of its own
			}
			if refersTo(p.TypesInfo.TypeOf(expr), t) {
				return true
			}
		}
		return false
	}
	for _, f := range p.Syntax {
		if isNonSourceFile(p.Fset.Position(f.Pos()).Filename) {
			continue
		}
		for _, decl := range f.Decls {
			switch d := decl.(type) {
			case *ast.GenDecl:
				for _, spec := range d.Specs {
					ts, ok := spec.(*ast.TypeSpec)
					if !ok {
						continue
					}
					st, ok := ts.Type.(*ast.StructType)
					if !ok {
						continue
					}
					if obj := p.TypesInfo.Defs[ts.Name]; obj != nil && implements(obj.Type()) {
						continue
					}
					if takes(st.Fields) {
						return true
					}
				}
			case *ast.FuncDecl:
				if d.Recv != nil && len(d.Recv.List) > 0 && implements(p.TypesInfo.TypeOf(d.Recv.List[0].Type)) {
					continue
				}
				if d.Type.Results != nil && takes(d.Type.Results) {
					continue
				}
				if takes(d.Type.Params) {
					return true
				}
			}
		}
	}
	return false
}

// refersTo reports whether typ is t, or a pointer, slice, array, map (key or
// value) or channel of it.
func refersTo(typ, t types.Type) bool {
	switch u := typ.(type) {
	case nil:
		return false
	case *types.Pointer:
		return refersTo(u.Elem(), t)
	case *types.Slice:
		return refersTo(u.Elem(), t)
	case *types.Array:
		return refersTo(u.Elem(), t)
	case *types.Map:
		return refersTo(u.Key(), t) || refersTo(u.Elem(), t)
	case *types.Chan:
		return refersTo(u.Elem(), t)
	}
	return types.Identical(typ, t)
}

// hasRegistryFunc reports whether p exports a `Register*` function — or a
// `Register*` method on one of its types (`(*Registry).Register`) — taking t:
// the strategy-registry shape the contracts skill names as a legitimate
// exclusion.
func hasRegistryFunc(p *packages.Package, t types.Type) bool {
	takesT := func(fn *types.Func) bool {
		if !fn.Exported() || !strings.HasPrefix(fn.Name(), "Register") {
			return false
		}
		params := fn.Type().(*types.Signature).Params()
		for i := 0; i < params.Len(); i++ {
			if types.Identical(params.At(i).Type(), t) {
				return true
			}
		}
		return false
	}
	scope := p.Types.Scope()
	for _, name := range scope.Names() {
		switch obj := scope.Lookup(name).(type) {
		case *types.Func:
			if takesT(obj) {
				return true
			}
		case *types.TypeName:
			named, ok := obj.Type().(*types.Named)
			if !ok {
				continue
			}
			for i := 0; i < named.NumMethods(); i++ {
				if takesT(named.Method(i)) {
					return true
				}
			}
		}
	}
	return false
}

// isTestSupportPackage reports whether p is test-support code: its non-test
// source imports "testing" (an httptest fake, a policy-enforcing test server).
// Such a package stands up fakes by design.
func isTestSupportPackage(p *packages.Package) bool {
	for _, f := range p.Syntax {
		if isNonSourceFile(p.Fset.Position(f.Pos()).Filename) {
			continue
		}
		for _, imp := range f.Imports {
			if strings.Trim(imp.Path.Value, `"`) == "testing" {
				return true
			}
		}
	}
	return false
}

// isStructurallyExemptKind reports whether p is a component kind forge's
// contract rules already exempt by STRUCTURE — shaped by a forge runtime
// interface, not by a Service: the app composition seam, a supervised worker,
// or a controller-runtime reconciler package (it declares a
// `SetupWithManager` method, the operator lifecycle). Those packages do I/O
// by nature, and the exclusion there is not what hides a missing contract.
func isStructurallyExemptKind(p *packages.Package) bool {
	path := p.PkgPath
	if path == "internal/app" || strings.HasSuffix(path, "/internal/app") {
		return true
	}
	if strings.Contains(path, "/internal/workers/") || strings.HasPrefix(path, "internal/workers/") {
		return hasMethods(p, workerLifecycleMethods...)
	}
	return hasMethods(p, "SetupWithManager")
}

// hasMethods reports whether some type declared in p's non-test source has
// every one of names as a method.
func hasMethods(p *packages.Package, names ...string) bool {
	found := map[string]bool{}
	for _, f := range p.Syntax {
		if isNonSourceFile(p.Fset.Position(f.Pos()).Filename) {
			continue
		}
		for _, decl := range f.Decls {
			fd, ok := decl.(*ast.FuncDecl)
			if ok && fd.Recv != nil && len(fd.Recv.List) > 0 {
				found[fd.Name.Name] = true
			}
		}
	}
	for _, n := range names {
		if !found[n] {
			return false
		}
	}
	return true
}

// isNonSourceFile reports whether file is test or generated code — neither
// is the package's own hand-written surface.
func isNonSourceFile(file string) bool {
	base := filepath.Base(file)
	return strings.HasSuffix(base, "_test.go") || strings.HasSuffix(base, "_gen.go") ||
		strings.HasPrefix(base, "zz_generated")
}

// pkgDir is p's source directory.
func pkgDir(p *packages.Package) string {
	for _, f := range p.GoFiles {
		return filepath.Dir(f)
	}
	return ""
}

func shortPos(pos token.Position) string {
	return fmt.Sprintf("%s:%d", filepath.Base(pos.Filename), pos.Line)
}

// applyDirectiveSuppressions honours the ordinary line/file suppression
// directives (internal/linter/suppress) in the MARKER's file, so a genuinely
// justified exception is written the same way as every other one —
// `// forge:lint-disable-next-line <rule>: <why>` above the marker — and a
// reasonless suppression of these error-severity rules is itself reported.
func applyDirectiveSuppressions(in []ExcludeFinding) []ExcludeFinding {
	byFile := map[string][]ExcludeFinding{}
	var order []string
	for _, f := range in {
		if _, ok := byFile[f.File]; !ok {
			order = append(order, f.File)
		}
		byFile[f.File] = append(byFile[f.File], f)
	}
	var out []ExcludeFinding
	for _, file := range order {
		fs := byFile[file]
		content, err := os.ReadFile(file)
		if err != nil {
			out = append(out, fs...)
			continue
		}
		lines := strings.Split(string(content), "\n")
		for _, f := range fs {
			kept, violations := suppressAtMarker(string(content), lines, f)
			if kept {
				out = append(out, f)
			}
			for _, v := range violations {
				out = append(out, ExcludeFinding{Rule: v.Rule, Severity: v.Severity, File: file, Line: v.Line, Message: v.Message, FixHint: v.Remediation})
			}
		}
	}
	return out
}

// suppressAtMarker applies the file's suppression directives to one finding
// anchored on the marker line. An allowance anywhere in the marker's comment
// block reaches it:
//
//   - ABOVE the marker, a next-line directive covers every comment line down
//     to the declaration (suppress's next-line span), so the marker is
//     covered even with a second stacked allowance, prose, or a blank `//`
//     in between. A marker that draws two refusals needs two allowances,
//     and the second necessarily sits between the first and the marker.
//   - BELOW the marker, in the same block, a next-line directive targets the
//     declaration the block documents. gofmt produces exactly this shape
//     when the two are spelled differently: it moves `//forge:` directive
//     lines to the end of a doc comment, so an unspaced allowance lands
//     under a spaced `// forge:exclude-contract:` marker. The finding is
//     therefore also checked at that declaration line.
//
// Everything else — reasons, the missing-reason violation, file/block
// scopes — is suppress.Apply's own.
func suppressAtMarker(content string, lines []string, f ExcludeFinding) (kept bool, violations []finding.Finding) {
	anchors := []int{f.Line}
	if decl := declLineAfterComment(lines, f.Line); decl != f.Line {
		anchors = append(anchors, decl)
	}
	for _, line := range anchors {
		res := suppress.Apply(content, []finding.Finding{{
			Rule: f.Rule, Severity: f.Severity, File: f.File, Line: line, Message: f.Message,
		}})
		if len(res.Kept) == 0 {
			return false, res.Violations
		}
	}
	return true, nil
}

// declLineAfterComment returns the first line after line that is not a `//`
// comment: the declaration the marker's comment block documents (the package
// clause, for a package doc). Returns line itself when there is none.
func declLineAfterComment(lines []string, line int) int {
	for l := line + 1; l <= len(lines); l++ {
		if !strings.HasPrefix(strings.TrimSpace(lines[l-1]), "//") {
			return l
		}
	}
	return line
}
