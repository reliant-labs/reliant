package svcerr_test

import (
	"errors"
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// wire_message_test.go — what a CLIENT reads when a service returns an
// svcerr.
//
// The sentinels in this package exist so `errors.Is` can classify a
// failure. Their strings were chosen to make that identity obvious in a
// debugger — "svcerr: not found" — and nobody looked at where those
// strings end up. connect.Error.Message() is literally err.Error(), so a
// constructor that formats "<detail>: <sentinel>" publishes the library's
// internal identity tag to every caller:
//
//	order demo-order-ok cannot become ORDER_STATUS_DELIVERED: svcerr: failed precondition
//	get order: order: svcerr: not found
//
// Nothing on the far side can use it. The Connect code already carries
// the category, and x-forge-error-reason carries the machine-readable
// one. forge's own frontend runtime measured the cost: it shipped a
// regex whose only job was to erase this tag before showing a message to
// a user (web-runtime/src/errors.ts). The server was writing framing the
// client had to un-write.
//
// So: the client-visible message is the DETAIL the application wrote,
// and nothing else.

// constructors is the call table these tests exercise. It is not the
// authority on which constructors exist —
// TestConstructors_TableMatchesThePackage derives that from the package
// source and fails if the two disagree, so a constructor added later
// cannot quietly go unexercised.
var constructors = map[string]func(string) error{
	"Canceled":            svcerr.Canceled,
	"Unknown":             svcerr.Unknown,
	"InvalidArgument":     svcerr.InvalidArgument,
	"DeadlineExceeded":    svcerr.DeadlineExceeded,
	"NotFound":            svcerr.NotFound,
	"AlreadyExists":       svcerr.AlreadyExists,
	"PermissionDenied":    svcerr.PermissionDenied,
	"ResourceExhausted":   svcerr.ResourceExhausted,
	"FailedPrecondition":  svcerr.FailedPrecondition,
	"Aborted":             svcerr.Aborted,
	"OutOfRange":          svcerr.OutOfRange,
	"Unimplemented":       svcerr.Unimplemented,
	"Internal":            svcerr.Internal,
	"Unavailable":         svcerr.Unavailable,
	"DataLoss":            svcerr.DataLoss,
	"Unauthenticated":     svcerr.Unauthenticated,
	"PlanLimit":           svcerr.PlanLimit,
	"InsufficientBalance": svcerr.InsufficientBalance,
	"Expired":             svcerr.Expired,
	"Draining":            svcerr.Draining,
	"ScaffoldStub":        svcerr.ScaffoldStub,
}

// packageFiles parses every non-test .go file of this package.
func packageFiles(t *testing.T, fset *token.FileSet) []*ast.File {
	t.Helper()
	paths, err := filepath.Glob("*.go")
	if err != nil {
		t.Fatalf("glob package source: %v", err)
	}
	var files []*ast.File
	for _, p := range paths {
		if strings.HasSuffix(p, "_test.go") {
			continue
		}
		f, err := parser.ParseFile(fset, p, nil, 0)
		if err != nil {
			t.Fatalf("parse %s: %v", p, err)
		}
		files = append(files, f)
	}
	if len(files) == 0 {
		t.Fatal("found ZERO source files for this package — the derivation is broken")
	}
	return files
}

// declaredConstructors reads this package's source and returns every
// exported `func(string) error` — the shape every category constructor
// has, and the only declaration in the package that has it. Deriving the
// set from the source is what makes the coverage assertion real: a name
// list would certify only the names someone remembered to add.
func declaredConstructors(t *testing.T) []string {
	t.Helper()
	var out []string
	for _, file := range packageFiles(t, token.NewFileSet()) {
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Recv != nil || !fn.Name.IsExported() {
				continue
			}
			if isIdent(fn.Type.Params, "string") && isIdent(fn.Type.Results, "error") {
				out = append(out, fn.Name.Name)
			}
		}
	}
	sort.Strings(out)
	return out
}

// isIdent reports whether a field list is exactly one unnamed-or-named
// parameter of the given builtin type.
func isIdent(fields *ast.FieldList, name string) bool {
	if fields == nil || len(fields.List) != 1 {
		return false
	}
	f := fields.List[0]
	if len(f.Names) > 1 {
		return false
	}
	id, ok := f.Type.(*ast.Ident)
	return ok && id.Name == name
}

func TestConstructors_TableMatchesThePackage(t *testing.T) {
	t.Parallel()
	declared := declaredConstructors(t)
	if len(declared) == 0 {
		t.Fatal("derived ZERO constructors from the package source — the derivation is broken, " +
			"and every assertion below would pass over an empty set")
	}
	var listed []string
	for name := range constructors {
		listed = append(listed, name)
	}
	sort.Strings(listed)
	if strings.Join(declared, ",") != strings.Join(listed, ",") {
		t.Fatalf("constructor table is out of date with the package:\n  declared: %v\n  exercised: %v", declared, listed)
	}
}

// TestConstructors_ClientMessageCarriesNoSentinelText is the guard.
func TestConstructors_ClientMessageCarriesNoSentinelText(t *testing.T) {
	t.Parallel()
	const detail = "the freezer door was left open"
	for name, ctor := range constructors {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			msg := svcerr.ToConnect(ctor(detail)).Message()
			if strings.Contains(strings.ToLower(msg), "svcerr") {
				t.Errorf("%s puts the library's errors.Is identity on the wire: %q", name, msg)
			}
			if !strings.Contains(msg, detail) {
				t.Errorf("%s lost the detail the application wrote: %q", name, msg)
			}
		})
	}
}

// TestConstructors_MessageIsExactlyTheDetail pins the stronger half for
// the constructors documented to take a complete detail: the library adds
// NOTHING. NotFound is excluded because its parameter is an ENTITY name,
// not a detail — it composes the sentence itself, which is what
// TestNotFound_ComposesASentence checks. ScaffoldStub is excluded for the
// same reason: its parameter is an RPC NAME, and it composes the sentence
// (TestScaffoldStub_ComposesASentence).
func TestConstructors_MessageIsExactlyTheDetail(t *testing.T) {
	t.Parallel()
	const detail = "no billing account"
	for name, ctor := range constructors {
		if name == "NotFound" || name == "ScaffoldStub" {
			continue
		}
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			if msg := svcerr.ToConnect(ctor(detail)).Message(); msg != detail {
				t.Errorf("%s message = %q, want exactly the detail %q", name, msg, detail)
			}
		})
	}
}

func TestNotFound_ComposesASentence(t *testing.T) {
	t.Parallel()
	if msg := svcerr.ToConnect(svcerr.NotFound("order")).Message(); msg != "order not found" {
		t.Errorf("NotFound message = %q, want a readable sentence built from the entity name", msg)
	}
}

func TestScaffoldStub_ComposesASentence(t *testing.T) {
	t.Parallel()
	if msg := svcerr.ToConnect(svcerr.ScaffoldStub("ShipOrder")).Message(); msg != "handler for ShipOrder not yet implemented" {
		t.Errorf("ScaffoldStub message = %q, want a readable sentence built from the RPC name", msg)
	}
}

// TestScaffoldStub_IsDistinctFromUnimplemented is the whole point of the
// sentinel. Both carry CodeUnimplemented — that is deliberate, the wire
// contract for "not implemented" does not change — but they must not be
// errors.Is-interchangeable, because the scaffold test row keys on the
// difference to tell "forge wrote this" from "somebody wrote this and it
// answers Unimplemented on purpose".
func TestScaffoldStub_IsDistinctFromUnimplemented(t *testing.T) {
	t.Parallel()
	stub := svcerr.ScaffoldStub("ShipOrder")
	if got := svcerr.ToConnect(stub).Code(); got != connect.CodeUnimplemented {
		t.Errorf("ScaffoldStub code = %s, want %s", got, connect.CodeUnimplemented)
	}
	if !errors.Is(stub, svcerr.ErrScaffoldStub) {
		t.Error("ScaffoldStub does not match its own sentinel")
	}
	if errors.Is(stub, svcerr.ErrUnimplemented) {
		t.Error("ScaffoldStub matches ErrUnimplemented — a hand-written Unimplemented would be indistinguishable from an unwritten handler")
	}
	if errors.Is(svcerr.Unimplemented("served by the forwarder"), svcerr.ErrScaffoldStub) {
		t.Error("a hand-written Unimplemented matches ErrScaffoldStub — the scaffold row would pass against an implemented handler")
	}
}

// TestScaffoldStub_CarriesReasonMetadata pins the half that survives the
// network. errors.Is cannot cross a Connect boundary (the error is
// marshalled and rebuilt), so the reason header is the only identification
// an integration-tier row has; if this regresses, those rows silently stop
// asserting anything.
func TestScaffoldStub_CarriesReasonMetadata(t *testing.T) {
	t.Parallel()
	ce := svcerr.ToConnect(svcerr.ScaffoldStub("ShipOrder"))
	if got := ce.Meta().Get(svcerr.ReasonHeader); got != svcerr.ReasonScaffoldStub {
		t.Errorf("%s = %q, want %q", svcerr.ReasonHeader, got, svcerr.ReasonScaffoldStub)
	}
}

// TestSentinels_CarryNoIdentityTag closes the other door: a service that
// returns a BARE sentinel (`return svcerr.ErrNotFound`) puts the
// sentinel's own text on the wire, so that text must be readable prose
// too. Asserted against the source rather than against values, so a
// sentinel added tomorrow is covered without being listed here.
func TestSentinels_CarryNoIdentityTag(t *testing.T) {
	t.Parallel()
	fset := token.NewFileSet()
	seen := 0
	for _, file := range packageFiles(t, fset) {
		ast.Inspect(file, func(n ast.Node) bool {
			call, ok := n.(*ast.CallExpr)
			if !ok || len(call.Args) != 1 {
				return true
			}
			sel, ok := call.Fun.(*ast.SelectorExpr)
			if !ok || sel.Sel.Name != "New" {
				return true
			}
			if id, ok := sel.X.(*ast.Ident); !ok || id.Name != "errors" {
				return true
			}
			lit, ok := call.Args[0].(*ast.BasicLit)
			if !ok || lit.Kind != token.STRING {
				return true
			}
			text, err := strconv.Unquote(lit.Value)
			if err != nil {
				return true
			}
			seen++
			if strings.Contains(strings.ToLower(text), "svcerr") {
				t.Errorf("sentinel %q at %s tags itself with the package name; a bare return puts that on the wire",
					text, fset.Position(lit.Pos()))
			}
			return true
		})
	}
	if seen == 0 {
		t.Fatal("found ZERO errors.New sentinels in the package source — the derivation is broken")
	}
}

// A bare sentinel still classifies, and still reads as prose.
func TestBareSentinel_IsStillClassifiedAndReadable(t *testing.T) {
	t.Parallel()
	ce := svcerr.ToConnect(svcerr.ErrNotFound)
	if ce.Code() != connect.CodeNotFound {
		t.Fatalf("code = %v, want CodeNotFound", ce.Code())
	}
	if strings.Contains(ce.Message(), "svcerr") {
		t.Errorf("bare sentinel message = %q", ce.Message())
	}
	if !errors.Is(ce, svcerr.ErrNotFound) {
		t.Error("the sentinel identity must survive the mapping")
	}
}

// TestWrappedSentinel_PublishesOnlyTheDetail is the half a tag fix does not
// reach, and the dangerous half.
//
// connect.Error.Message() is err.Error(), and clientSafe used to treat a
// RECOGNISED sentinel as making the entire accumulated string client-safe.
// That is false the moment anyone wraps — and the service-layer skill tells
// them to:
//
//	return Thing{}, fmt.Errorf("get thing: %w", err)
//
// So an operator's context — ids, internal hostnames, a DSN with a password —
// rode to an unauthenticated caller behind a sentinel that was only ever
// meant to pick a status code. The message a client reads is the DETAIL the
// constructor was given, however many layers of context were wrapped around
// it on the way out; the context stays reachable server-side as a cause.
func TestWrappedSentinel_PublishesOnlyTheDetail(t *testing.T) {
	t.Parallel()
	const secret = "dsn=postgres://app:s3cr3t@db.internal:5432/prod"
	inner := svcerr.NotFound("order")
	wrapped := fmt.Errorf("get order ord_01H from shard-7 (%s): %w", secret, inner)

	ce := svcerr.ToConnect(wrapped)
	if ce.Code() != connect.CodeNotFound {
		t.Fatalf("code = %v, want CodeNotFound — wrapping must not change the classification", ce.Code())
	}
	if ce.Message() != "order not found" {
		t.Errorf("message = %q, want only the detail the constructor was given", ce.Message())
	}
	for _, leak := range []string{secret, "s3cr3t", "shard-7", "ord_01H"} {
		if strings.Contains(ce.Message(), leak) {
			t.Errorf("wrapping context %q reached the client: %q", leak, ce.Message())
		}
	}
	// The operator still gets everything: the full chain is the cause, and
	// errors.Is keeps working through it.
	cause := svcerr.Cause(ce)
	if cause == nil || !strings.Contains(cause.Error(), secret) {
		t.Errorf("the diagnostic must survive server-side; cause = %v", cause)
	}
	if !errors.Is(ce, svcerr.ErrNotFound) {
		t.Error("the sentinel identity must survive the mapping")
	}
}

// The same hole with a BARE sentinel, which carries no detail at all.
func TestWrappedBareSentinel_PublishesOnlyTheCategory(t *testing.T) {
	t.Parallel()
	const secret = "dsn=postgres://app:s3cr3t@db.internal:5432/prod"
	ce := svcerr.ToConnect(fmt.Errorf("load config (%s): %w", secret, svcerr.ErrFailedPrecondition))
	if ce.Code() != connect.CodeFailedPrecondition {
		t.Fatalf("code = %v, want CodeFailedPrecondition", ce.Code())
	}
	if ce.Message() != "failed precondition" {
		t.Errorf("message = %q, want the sentinel's own text and nothing wrapped around it", ce.Message())
	}
	if strings.Contains(ce.Message(), "s3cr3t") {
		t.Errorf("wrapping context reached the client: %q", ce.Message())
	}
}
