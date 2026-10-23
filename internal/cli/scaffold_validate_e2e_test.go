//go:build e2e

package cli

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"testing"
	"time"
)

// TestE2EValidateConstraintsProjection is the acceptance gate for
// protovalidate-driven field validation: ONE declaration on a proto field
// (`[(buf.validate.field)...]`) projects to THREE enforcement points —
//
//  1. a DB CHECK constraint in the born migration,
//  2. a zod validator in the generated create/edit form, and
//  3. the protovalidate wire interceptor, mounted in the serve wiring and
//     rejecting invalid requests with InvalidArgument.
//
// Point 3 is asserted on the message an RPC actually receives, not just on
// the entity: Update NESTS the entity (the interceptor recurses into it),
// but Create FLATTENS the entity's fields into its own message, so the
// rules are enforced there only if the birth copied them across. A Create
// missing them is the worst of the three failures — the request passes the
// wire, reaches the DB, trips the CHECK, and returns Internal for what was
// a client mistake.
//
// The entity carries the whole projected subset: an int lower bound, a
// string min/max length, a string pattern, an email, and a required
// field — plus a `plain` field with NO rules as the negative control.
//
// RED before this feature: today's fresh project births NO validate CHECK
// constraints (only the id/enum ones), the form fields are bare
// `z.string()` / `z.coerce.number()` with no refinements, and there is no
// validation interceptor in cmd/serve.go — so the invalid request would
// reach the handler.
//
// Runs the REAL pipeline (buf compile with the vendored validate.proto +
// embedded-postgres shadow apply), asserts all three enforcement points,
// requires generate x2 idempotent + go build/vet green, executes a wire
// interceptor rejection against the REAL generated message types, and —
// when node is present — type-checks the generated frontend.
func TestE2EValidateConstraintsProjection(t *testing.T) {
	t.Parallel() // independent project in its own t.TempDir; binary shared via sync.Once
	forgeBin := buildforgeBinary(t)
	dir := t.TempDir()

	runCmd(t, dir, forgeBin,
		"project", "new", "shopapp",
		"--mod", "example.com/shopapp",
		"--service", "catalog",
		"--frontend", "dashboard",
	)
	projectDir := filepath.Join(dir, "shopapp")
	addCorpusForgePkgReplace(t, projectDir)

	// Author the marked entity with the full protovalidate subset. The
	// validate.proto import is inserted after the syntax line; forge
	// vendors buf/validate/validate.proto on the first buf run.
	protoPath := filepath.Join(projectDir, "proto", "services", "catalog", "v1", "catalog.proto")
	proto := readFileE2E(t, protoPath)
	if !strings.Contains(proto, `import "buf/validate/validate.proto";`) {
		proto = strings.Replace(proto, `syntax = "proto3";`,
			`syntax = "proto3";`+"\n"+`import "buf/validate/validate.proto";`, 1)
	}
	proto += `
// forge:entity
message Widget {
  string id = 1;
  int64 amount_cents = 2 [(buf.validate.field).int64.gte = 0];
  string name = 3 [(buf.validate.field).string.min_len = 2, (buf.validate.field).string.max_len = 64];
  string sku = 4 [(buf.validate.field).string.pattern = "^SKU-[0-9]+$"];
  string email = 5 [(buf.validate.field).string.email = true];
  string code = 6 [(buf.validate.field).required = true];
  int64 count = 7;
  // The int lower bound is carried on an int32 as well as the int64 above:
  // int32 is TS number, so its zod value is z.coerce.number() and the bound
  // projects as a numeric refinement. int64 is TS bigint, whose zod value is
  // a DIGITS-STRING (a number input would round off large values), and a
  // numeric refinement cannot be chained onto it — see tsZodBase.
  int32 quantity = 9 [(buf.validate.field).int32.gte = 0];
  // Fixed-length ISO currency code: the EXACT-length form buf lint requires
  // (min_len == max_len is rejected in favor of string.len / string.const).
  string currency = 8 [(buf.validate.field).string.len = 3];
}
`
	if err := os.WriteFile(protoPath, []byte(proto), 0o644); err != nil {
		t.Fatalf("author catalog proto: %v", err)
	}

	// Phase 1 births the widgets table (from the raw scan); phase 2 runs
	// the full generate pipeline (descriptor + Go + TS + born pages).
	runCmd(t, projectDir, forgeBin, "scaffold")

	// ── ENFORCEMENT POINT 1: DB CHECK in the born migration ──
	up := readBornMigrationE2E(t, projectDir, "widgets")
	for _, want := range []string{
		"amount_cents BIGINT NOT NULL DEFAULT 0 CHECK (amount_cents >= 0)",
		"name TEXT NOT NULL CHECK (char_length(name) BETWEEN 2 AND 64)",
		"sku TEXT NOT NULL CHECK (sku ~ '^SKU-[0-9]+$')",
		"email TEXT NOT NULL CHECK (email ~ '^[^@",
		"code TEXT NOT NULL CHECK (char_length(code) >= 1)",
		// Fixed length projects to a single exact CHECK, never `BETWEEN 3 AND 3`.
		"currency TEXT NOT NULL CHECK (char_length(currency) = 3)",
	} {
		if !strings.Contains(up, want) {
			t.Errorf("born migration missing CHECK %q:\n%s", want, up)
		}
	}
	// Negative control: the unconstrained field keeps a plain column, no CHECK.
	if !strings.Contains(up, "count BIGINT NOT NULL DEFAULT 0") || strings.Contains(up, "CHECK (count") {
		t.Errorf("unconstrained `count` field must get a plain column and no validate CHECK:\n%s", up)
	}

	// ── ENFORCEMENT POINT 3: zod validators in the born forms ──
	appDir := filepath.Join(projectDir, "frontends", "dashboard", "src", "app")
	createPath := filepath.Join(appDir, "widgets", "new", "page.tsx")
	assertPathExistsE2E(t, createPath)
	create := readFileE2E(t, createPath)
	for _, want := range []string{
		"quantity: z.coerce.number().gte(0),",
		"name: z.string().min(2).max(64),",
		`sku: z.string().regex(new RegExp("^SKU-[0-9]+$")),`,
		"email: z.string().email(),",
		"code: z.string().min(1),",
		"currency: z.string().min(3).max(3),",
	} {
		if !strings.Contains(create, want) {
			t.Errorf("born create form missing zod validator %q:\n%s", want, create)
		}
	}
	// Negative control: the unconstrained numeric field carries no
	// refinement beyond the base expression for its type. `count` is int64,
	// so that base is the bigint digits-string, not z.coerce.number().
	if !strings.Contains(create, `count: z.string().regex(/^(-?\d+)?$/, "expected a whole number")`) {
		t.Errorf("unconstrained `count` field must keep the bare bigint base expression:\n%s", create)
	}
	if strings.Contains(create, "count: z.coerce.number().gte") || strings.Contains(create, "count: z.string().regex(/^(-?\\d+)?$/, \"expected a whole number\").gte") {
		t.Errorf("unconstrained `count` field must carry no numeric refinement:\n%s", create)
	}
	// The edit form (fields sourced from the entity itself) carries them too.
	editPath := filepath.Join(appDir, "widgets", "edit", "page.tsx")
	assertPathExistsE2E(t, editPath)
	edit := readFileE2E(t, editPath)
	for _, want := range []string{"name: z.string().min(2).max(64),", "quantity: z.coerce.number().gte(0),"} {
		if !strings.Contains(edit, want) {
			t.Errorf("born edit form missing zod validator %q:\n%s", want, edit)
		}
	}

	// ── ENFORCEMENT POINT 2a: the rules reach the RPC INPUT message ──
	// The interceptor validates the message it is handed. Update wraps the
	// entity (protovalidate recurses into it), but Create FLATTENS the
	// entity's fields into its own message — so the rules only exist there
	// if the birth copied them. Without this the whole enforcement point is
	// decorative for the one RPC that takes client-authored values.
	protoAfter := readFileE2E(t, protoPath)
	createReq := protoMessageBlockE2E(t, protoAfter, "CreateWidgetRequest")
	for _, want := range []string{
		"amount_cents = 1 [(buf.validate.field).int64.gte = 0]",
		"name = 2 [(buf.validate.field).string.min_len = 2, (buf.validate.field).string.max_len = 64]",
		`sku = 3 [(buf.validate.field).string.pattern = "^SKU-[0-9]+$"]`,
		"email = 4 [(buf.validate.field).string.email = true]",
		"code = 5 [(buf.validate.field).required = true]",
		"quantity = 7 [(buf.validate.field).int32.gte = 0]",
		"currency = 8 [(buf.validate.field).string.len = 3]",
	} {
		if !strings.Contains(createReq, want) {
			t.Errorf("CreateWidgetRequest missing field rules %q:\n%s", want, createReq)
		}
	}
	// Negative control: the unconstrained field stays bare — rules are
	// copied, never invented.
	if !strings.Contains(createReq, "int64 count = 6;") {
		t.Errorf("unconstrained `count` must stay a bare Create field:\n%s", createReq)
	}
	// The nesting shape needs none of this, and must not have grown any.
	if updReq := protoMessageBlockE2E(t, protoAfter, "UpdateWidgetRequest"); strings.Contains(updReq, "buf.validate.field") {
		t.Errorf("UpdateWidgetRequest nests the entity — it must carry no field rules:\n%s", updReq)
	}

	// ── ENFORCEMENT POINT 2: the wire interceptor is mounted ──
	// A new project composes with the graph: the API listener serves every
	// user route through the UserChain provider, which serverkit.NewChain
	// builds. Its Validate slot defaults to protovalidate (pkg/validate) and
	// only serverkit.NoValidate drops it, so the interceptor is mounted
	// exactly when providers.go builds the chain with NewChain and never
	// names NoValidate. serverkit's own tests pin the default slot.
	providers := readFileE2E(t, filepath.Join(projectDir, "internal", "app", "providers.go"))
	if !strings.Contains(providers, "serverkit.NewChain(serverkit.ChainDeps{") {
		t.Errorf("internal/app/providers.go does not build the user chain with serverkit.NewChain, whose default Validate slot is the protovalidate interceptor:\n%s", providers)
	}
	if strings.Contains(providers, "serverkit.NoValidate") {
		t.Errorf("internal/app/providers.go drops the protovalidate interceptor (serverkit.NoValidate):\n%s", providers)
	}

	// Inject a runtime test that drives the REAL generated message types
	// through OUR interceptor: an invalid Widget must be rejected with
	// InvalidArgument before the handler runs; a valid one must pass.
	writeWireRejectionTest(t, projectDir)

	// ── generate x2: green and byte-for-byte idempotent ──
	snaps := make([]map[string]string, 0, 2)
	for i := 0; i < 2; i++ {
		runCmd(t, projectDir, forgeBin, "generate")
		snaps = append(snaps, hashProjectTree(t, projectDir))
		runCmd(t, projectDir, "go", "build", "./...")
		runCmd(t, projectDir, "go", "vet", "./...")
	}
	if diff := diffTreeE2E(snaps[0], snaps[1]); diff != "" {
		t.Errorf("generate #2 is not idempotent vs #1 (file churn):\n%s", diff)
	}

	// ── runtime: the interceptor rejects an invalid request ──
	runCmd(t, projectDir, "go", "test", "./internal/wirevalidate/")

	// ── frontend type-check (the `forge build` frontend gate) ──
	// A bare `return` here used to drop the frontend type-check and still
	// report PASS. requireTool makes the same absence a hard failure in CI.
	requireTool(t, "node", "npm")
	webDir := filepath.Join(projectDir, "frontends", "dashboard")
	// The install resolves @reliantlabs/forge-web-runtime over a file: link
	// into the shared repo checkout and runs its prepare script there; build
	// it once up front so parallel tests do not race that bootstrap.
	prebuildWebRuntimeE2E(t)
	runCmdTimeout(t, webDir, 5*time.Minute, "npm", "install", "--no-audit", "--no-fund", "--prefer-offline")

	// Generate AGAIN, after the install. The buf TypeScript pass runs
	// protoc-gen-es out of the frontend's OWN node_modules and, when it is
	// absent, warns and skips rather than failing — so on a machine whose
	// npm cache is cold enough that `forge project new`'s best-effort
	// install did not finish, every generate above emitted no src/gen/ at
	// all, and the type-check below died with TS2307 on
	// "@/gen/services/catalog/v1/catalog_pb" for a reason that has nothing
	// to do with validation. Green locally, red in CI, purely on cache
	// warmth. This run is a no-op when the stubs are already there.
	runCmd(t, projectDir, forgeBin, "generate")
	assertPathExistsE2E(t, filepath.Join(webDir, "src", "gen", "services", "catalog", "v1", "catalog_pb.ts"))

	runCmdTimeout(t, webDir, 3*time.Minute, "npx", "--no-install", "tsc", "--noEmit")
}

// writeWireRejectionTest drops a standalone test package into the generated
// project that builds pkg/validate.Interceptor() and drives the REAL
// generated Widget type through it — proving the wire enforcement point end
// to end against the compiled buf.validate rules.
func writeWireRejectionTest(t *testing.T, projectDir string) {
	t.Helper()
	born := readFileE2E(t, filepath.Join(projectDir, "internal", "handlers", "catalog", "handlers_crud_test.go"))
	pbImport := regexp.MustCompile(`pb "([^"]+)"`).FindStringSubmatch(born)
	if pbImport == nil {
		t.Fatalf("born handlers_crud_test.go carries no pb import:\n%s", born)
	}

	test := fmt.Sprintf(`package wirevalidate

// Written by forge's validate e2e (TestE2EValidateConstraintsProjection):
// proves the protovalidate interceptor rejects a Widget violating its
// buf.validate rules with InvalidArgument, and passes a valid one — using
// the REAL generated message types and the shared pkg/validate library.

import (
	"context"
	"strings"
	"testing"

	"connectrpc.com/connect"

	pb "%s"
	"github.com/reliant-labs/forge/pkg/validate"
)

func TestWireInterceptorRejectsInvalidWidget(t *testing.T) {
	inter, err := validate.Interceptor()
	if err != nil {
		t.Fatalf("build interceptor: %%v", err)
	}
	handlerRan := false
	next := connect.UnaryFunc(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		handlerRan = true
		return connect.NewResponse(&pb.Widget{}), nil
	})

	// amount_cents < 0 violates (buf.validate.field).int64.gte = 0.
	_, err = inter.WrapUnary(next)(context.Background(), connect.NewRequest(&pb.Widget{Id: "x", AmountCents: -5}))
	if err == nil || connect.CodeOf(err) != connect.CodeInvalidArgument {
		t.Fatalf("invalid Widget: got err=%%v code=%%v, want InvalidArgument", err, connect.CodeOf(err))
	}
	if handlerRan {
		t.Fatal("handler ran despite an invalid request — the interceptor did not gate it")
	}

	// A fully valid Widget passes every rule (currency is an exact 3 chars —
	// string.len = 3 — so a shorter/longer value would be rejected).
	good := &pb.Widget{Id: "x", AmountCents: 10, Name: "ok", Sku: "SKU-1", Email: "a@b.com", Code: "c", Currency: "USD"}
	if _, err := inter.WrapUnary(next)(context.Background(), connect.NewRequest(good)); err != nil {
		t.Fatalf("valid Widget rejected: %%v", err)
	}
	if !handlerRan {
		t.Fatal("handler did not run for a valid request")
	}
}

// TestWireInterceptorRejectsInvalidCreateRequest drives the message an RPC
// actually receives. CreateWidgetRequest FLATTENS the entity's fields
// instead of nesting the entity, so protovalidate enforces nothing on it
// unless the birth copied the rules across — and an over-length name would
// otherwise pass the wire, reach the DB, trip its CHECK, and surface as
// Internal: a 500-class error for a client mistake.
func TestWireInterceptorRejectsInvalidCreateRequest(t *testing.T) {
	inter, err := validate.Interceptor()
	if err != nil {
		t.Fatalf("build interceptor: %%v", err)
	}
	handlerRan := false
	next := connect.UnaryFunc(func(context.Context, connect.AnyRequest) (connect.AnyResponse, error) {
		handlerRan = true
		return connect.NewResponse(&pb.CreateWidgetResponse{}), nil
	})

	valid := func() *pb.CreateWidgetRequest {
		return &pb.CreateWidgetRequest{
			AmountCents: 10, Name: "ok", Sku: "SKU-1", Email: "a@b.com", Code: "c", Currency: "USD",
		}
	}
	for _, tc := range []struct {
		name string
		req  *pb.CreateWidgetRequest
	}{
		{"over-length name", func() *pb.CreateWidgetRequest {
			r := valid()
			r.Name = strings.Repeat("x", 65)
			return r
		}()},
		{"negative amount", func() *pb.CreateWidgetRequest { r := valid(); r.AmountCents = -5; return r }()},
		{"pattern mismatch", func() *pb.CreateWidgetRequest { r := valid(); r.Sku = "nope"; return r }()},
		{"bad email", func() *pb.CreateWidgetRequest { r := valid(); r.Email = "not-an-email"; return r }()},
		{"wrong fixed length", func() *pb.CreateWidgetRequest { r := valid(); r.Currency = "US"; return r }()},
	} {
		handlerRan = false
		_, err := inter.WrapUnary(next)(context.Background(), connect.NewRequest(tc.req))
		if err == nil || connect.CodeOf(err) != connect.CodeInvalidArgument {
			t.Errorf("%%s: got err=%%v code=%%v, want InvalidArgument at the wire", tc.name, err, connect.CodeOf(err))
		}
		if handlerRan {
			t.Errorf("%%s: handler ran — the request reached the DB path instead of being gated", tc.name)
		}
	}

	handlerRan = false
	if _, err := inter.WrapUnary(next)(context.Background(), connect.NewRequest(valid())); err != nil {
		t.Fatalf("valid CreateWidgetRequest rejected: %%v", err)
	}
	if !handlerRan {
		t.Fatal("handler did not run for a valid CreateWidgetRequest")
	}
}
`, pbImport[1])

	dir := filepath.Join(projectDir, "internal", "wirevalidate")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("mkdir wirevalidate: %v", err)
	}
	if err := os.WriteFile(filepath.Join(dir, "wire_test.go"), []byte(test), 0o644); err != nil {
		t.Fatalf("write wire rejection test: %v", err)
	}
}

// protoMessageBlockE2E returns the brace-matched text of
// `message <name> { ... }` from a proto file.
func protoMessageBlockE2E(t *testing.T, proto, name string) string {
	t.Helper()
	start := strings.Index(proto, "message "+name+" {")
	if start < 0 {
		t.Fatalf("message %s not found in the proto:\n%s", name, proto)
	}
	depth := 0
	for i := start; i < len(proto); i++ {
		switch proto[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return proto[start : i+1]
			}
		}
	}
	t.Fatalf("message %s is unterminated:\n%s", name, proto)
	return ""
}
