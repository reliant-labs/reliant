package contract

import (
	"go/ast"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestClassifyParam is the redaction table: what a failed or slow call's
// forensics event may record of each parameter, decided from its type
// expression and name alone. Deny by default — anything not listed as a
// value records its length or its type only.
func TestClassifyParam(t *testing.T) {
	imports := map[string]string{
		"context":   "context",
		"time":      "time",
		"pb":        "example.com/shop/gen/orders/v1",
		"bufpb":     "buf.build/gen/go/acme/apis/protocolbuffers/go/acme/v1",
		"tspb":      "google.golang.org/protobuf/types/known/timestamppb",
		"domain":    "example.com/shop/internal/domain",
		"generator": "example.com/shop/internal/generator", // "gen" is not a path segment here
		"units":     "example.com/shop/internal/units",
	}
	aliases := map[string]string{"Status": "int", "OrderRef": "string", "Email": "string", "OTPCode": "int64", "Chained": "Status", "Wave": "complex128"}
	for _, tc := range []struct {
		name, typ string
		want      captureKind
	}{
		{"ctx", "context.Context", captureNone},
		{"n", "int", captureInt},
		{"n", "int64", captureInt},
		{"r", "rune", captureInt},
		{"n", "uint32", captureUint},
		{"b", "byte", captureUint},
		{"f", "float64", captureFloat},
		{"ok", "bool", captureBool},
		{"wait", "time.Duration", captureDuration},
		{"at", "time.Time", captureTime},
		{"orderID", "string", captureID},
		{"order_id", "string", captureID},
		{"userIDs", "string", captureID},
		{"id", "string", captureID},
		{"ids", "string", captureID},
		{"email", "string", captureLen},
		{"note", "string", captureLen},
		{"sessionID", "string", captureLen}, // a credential, ID-shaped or not
		{"apiToken", "string", captureLen},
		{"body", "[]byte", captureLen},
		{"ids", "[]string", captureCount},
		{"attrs", "map[string]string", captureCount},
		{"grid", "[4]int", captureCount},
		{"rest", "...string", captureCount},
		{"in", "*pb.Order", captureProto},
		{"in", "*bufpb.Order", captureProto},
		{"at", "*tspb.Timestamp", captureProto},
		{"status", "pb.Status", captureNamed},
		{"in", "*domain.Order", captureType},
		{"in", "domain.Order", captureType},
		{"in", "*generator.Spec", captureType},
		{"in", "*Order", captureType},
		{"in", "Order", captureType},
		{"err", "error", captureType},
		{"v", "any", captureType},
		{"fn", "func(int) error", captureType},
		{"ch", "chan int", captureType},
		{"p", "*int", captureType},
		{"s", "Status", captureInt},     // local named int
		{"ref", "OrderRef", captureLen}, // local named string, not ID-named
		{"orderRefID", "OrderRef", captureID},
		{"c", "complex128", captureType},
		// A secret-shaped ID name hides a number's value too: digit count only.
		{"otpCounterID", "int64", captureDigits},
		{"otp_counter_id", "uint32", captureDigits},
		{"sessionID", "float64", captureDigits},
		{"sessionID", "Status", captureDigits}, // local named int
		{"counterID", "int64", captureInt},
		{"tokenOK", "bool", captureBool},
		// A secret name hides a number's value whether or not it is
		// ID-shaped: a PIN, a CVV, a security code record their digit count.
		{"pin", "int", captureDigits},
		{"userPIN", "int32", captureDigits},
		{"cvv", "uint16", captureDigits},
		{"cvv2", "uint64", captureDigits},
		{"securityCode", "int64", captureDigits},
		{"ssn", "Status", captureDigits}, // local named int
		{"tan", "float64", captureDigits},
		// A quantity (first or last word max, min, limit, count, num,
		// total, size, len, length) is not a secret, for numbers only.
		{"maxTokens", "int", captureInt},
		{"max_tokens", "int32", captureInt},
		{"session_count", "uint64", captureUint},
		{"token_limit", "float64", captureFloat},
		{"num_sessions", "int", captureInt},
		{"numSessions", "int64", captureInt},
		{"pin_num", "int", captureDigits}, // a trailing `num` is "number", not a count
		{"ssn_num", "uint64", captureDigits},
		{"otpNum", "int32", captureDigits},
		{"otp_counter_id", "int64", captureDigits}, // last word `id`
		{"max_session_id", "int64", captureDigits}, // ID-shaped: never a quantity
		{"num_otp_id", "uint64", captureDigits},
		{"maxSessionID", "int", captureDigits},
		{"max_SESSIONID", "int64", captureDigits}, // ID-shaped in the broader legacy shape
		{"maxSESSIONID", "uint32", captureDigits},
		{"tokenCounter", "int", captureDigits}, // `counter` is not `count`
		{"maxTokens", "string", captureLen},
		{"session_count", "Status", captureInt}, // local named int
		{"spinner", "int", captureInt},          // `pin` matches a whole word only
		{"shipping", "uint32", captureUint},
		{"pin", "string", captureLen},
		{"pinID", "string", captureLen}, // ID-shaped, but secret
		{"mfaEnabled", "bool", captureBool},
		// Named types fail closed: a value only through a resolved local
		// primitive, and still under the secret-name rule.
		{"otpID", "OTPCode", captureDigits},      // local int64, secret-shaped name
		{"retries", "OTPCode", captureInt},       // same type, plain name
		{"otpID", "units.Cents", captureType},    // imported numeric: unresolved
		{"amountID", "units.Cents", captureType}, // unresolved even under a plain ID name
		{"otpID", "Chained", captureType},        // a named type over a named type
		{"otpID", "Unknown", captureType},        // declared nowhere this generator reads
		{"w", "Wave", captureType},               // resolved, but not a value it records
	} {
		if got := classifyParam(tc.name, tc.typ, imports, aliases); got != tc.want {
			t.Errorf("classifyParam(%q, %q) = %s, want %s", tc.name, tc.typ, got, tc.want)
		}
	}
}

func writeModule(t *testing.T, files map[string]string) (root, dir string) {
	t.Helper()
	root = t.TempDir()
	dir = filepath.Join(root, "internal", "orders")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	files["../../go.mod"] = "module example.com/shop\n\ngo 1.27\n"
	for name, src := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(src), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	return root, dir
}

func renderOrders(t *testing.T, dir string, opts Options) string {
	t.Helper()
	cf, err := ParseContract(filepath.Join(dir, "contract.go"))
	if err != nil {
		t.Fatal(err)
	}
	ok, err := WriteObservedDecorator(cf, dir, "Service", "NewServiceWithForgeMiddleware", "forgeMiddlewareService", "orders", &DefaultChain{Scope: "example.com/shop/internal/orders", Name: "orders"}, opts)
	if err != nil || !ok {
		t.Fatalf("generated=%v err=%v", ok, err)
	}
	raw, err := os.ReadFile(filepath.Join(dir, "middleware_gen.go"))
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

const ordersContract = `package orders

import (
	"context"
	"time"
)

type Service interface {
	// Place takes an order.
	// forge:no-capture coupon
	Place(ctx context.Context, orderID string, note string, qty int, coupon string, wait time.Duration, items []string, in *Input) (string, error)
	// Verify carries a one-time-password counter: its digit count only.
	Verify(ctx context.Context, otpCounterID int64, attempt uint8) error
	Get(ctx context.Context, orderID string) error
	// Ping is answered by an embedded helper.
	Ping(ctx context.Context) error
}

type Input struct{ SKU string }
`

func TestObservedDecoratorCodeLocationAndCapture(t *testing.T) {
	_, dir := writeModule(t, map[string]string{
		"contract.go": ordersContract,
		"service.go": `package orders

import (
	"context"
	"time"
)

type service struct{ pinger }

func New() Service { return &service{} }

func (s *service) Place(ctx context.Context, orderID, note string, qty int, coupon string, wait time.Duration, items []string, in *Input) (string, error) {
	return "", nil
}

func (s *service) Verify(ctx context.Context, otpCounterID int64, attempt uint8) error { return nil }
`,
		"get.go": `package orders

import "context"

// Get is in a file of its own.
// forge:no-capture
func (s *service) Get(ctx context.Context, orderID string) error { return nil }
`,
		"ping.go": `package orders

import "context"

type pinger struct{}

func (pinger) Ping(context.Context) error { return nil }
`,
		// A mock implements every method too; it must never be taken for the
		// implementation.
		"mock_gen.go": `package orders

import (
	"context"
	"time"
)

type MockService struct{}

func (m *MockService) Place(ctx context.Context, orderID, note string, qty int, coupon string, wait time.Duration, items []string, in *Input) (string, error) {
	return "", nil
}
func (m *MockService) Get(ctx context.Context, orderID string) error { return nil }
func (m *MockService) Ping(ctx context.Context) error             { return nil }
func (m *MockService) Verify(ctx context.Context, otpCounterID int64, attempt uint8) error { return nil }
`,
	})
	got := renderOrders(t, dir, Options{})
	for _, want := range []string{
		`Place:  observe.NewOp("orders.Place", "example.com/shop/internal/orders.Service.Place", "internal/orders/service.go"),`,
		`Get:    observe.NewOp("orders.Get", "example.com/shop/internal/orders.Service.Get", "internal/orders/get.go"),`,
		// Ping is promoted from an embedded type: tied to the one type that
		// declares it.
		`Ping:   observe.NewOp("orders.Ping", "example.com/shop/internal/orders.Service.Ping", "internal/orders/ping.go"),`,
		`observe.AroundOp(o.chain, ctx, forgeMiddlewareServiceOps.Place, forgeMiddlewareServicePlaceArgs{orderID, note, qty, items}, func(ctx context.Context) (string, error) {`,
		`r.ID("orderID", a.orderID)`,
		`r.Len("note", len(a.note))`,
		`r.Int("qty", int64(a.qty))`,
		`r.Type("wait", "Duration")`,
		`r.Count("items", len(a.items))`,
		`r.Type("in", "*Input")`,
		`r.IntDigits("otpCounterID", a.otpCounterID)`,
		`r.Uint("attempt", uint64(a.attempt))`,
		// Bare forge:no-capture on the implementation: nothing for Get.
		`observe.RunOp(o.chain, ctx, forgeMiddlewareServiceOps.Get, observe.NoArgs{}, func(ctx context.Context) error {`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("decorator missing %q\n---\n%s", want, got)
		}
	}
	if strings.Contains(got, `"coupon"`) {
		t.Errorf("forge:no-capture coupon still captured:\n%s", got)
	}
	if strings.Contains(got, "a.wait") || strings.Contains(got, "\twait ") {
		t.Errorf("a duration's value is stored or recorded:\n%s", got)
	}
	if strings.Contains(got, "\tin      *Input") || strings.Contains(got, "\tin *Input") {
		t.Errorf("a type-only parameter is stored in the capture:\n%s", got)
	}
	if strings.Contains(got, ".go:") {
		t.Errorf("a code location carries a line number:\n%s", got)
	}

	off := renderOrders(t, dir, Options{NoForensics: true})
	if strings.Contains(off, "RecordForensics") || strings.Contains(off, "PlaceArgs") {
		t.Errorf("NoForensics still generated capture code:\n%s", off)
	}
	if !strings.Contains(off, `observe.AroundOp(o.chain, ctx, forgeMiddlewareServiceOps.Place, observe.NoArgs{},`) {
		t.Errorf("NoForensics must still carry the op:\n%s", off)
	}
}

// Without an implementation in the package the interface's own file is the
// code location.
func TestObservedDecoratorCodeLocationFallsBackToTheInterface(t *testing.T) {
	_, dir := writeModule(t, map[string]string{"contract.go": ordersContract})
	got := renderOrders(t, dir, Options{})
	if !strings.Contains(got, `"example.com/shop/internal/orders.Service.Get", "internal/orders/contract.go")`) {
		t.Errorf("fallback location missing:\n%s", got)
	}
}

// Two types implementing the whole interface: the one New returns wins.
func TestObservedDecoratorCodeLocationPrefersTheReturnedType(t *testing.T) {
	_, dir := writeModule(t, map[string]string{
		"contract.go": "package orders\nimport \"context\"\ntype Service interface{ Get(ctx context.Context, orderID string) error }\n",
		"real.go":     "package orders\nimport \"context\"\ntype real struct{}\nfunc New() Service { return &real{} }\nfunc (*real) Get(context.Context, string) error { return nil }\n",
		"fake.go":     "package orders\nimport \"context\"\ntype fake struct{}\nfunc (fake) Get(context.Context, string) error { return nil }\n",
	})
	got := renderOrders(t, dir, Options{})
	if !strings.Contains(got, `"internal/orders/real.go")`) {
		t.Errorf("returned type not preferred:\n%s", got)
	}
}

func TestParseNoCapture(t *testing.T) {
	for text, want := range map[string]noCapture{
		"// forge:no-capture":                {all: true},
		"// forge:no-capture coupon":         {params: map[string]bool{"coupon": true}},
		"// forge:no-capture: a, b":          {params: map[string]bool{"a": true, "b": true}},
		"//forge:no-capture a b":             {params: map[string]bool{"a": true, "b": true}},
		"// forge:no-captured x":             {},
		"// see forge:no-capture for detail": {},
	} {
		got := parseNoCapture(commentGroup(text))
		if got.all != want.all || len(got.params) != len(want.params) {
			t.Errorf("parseNoCapture(%q) = %+v, want %+v", text, got, want)
			continue
		}
		for p := range want.params {
			if !got.params[p] {
				t.Errorf("parseNoCapture(%q) missing %q", text, p)
			}
		}
	}
}

func commentGroup(text string) *ast.CommentGroup {
	return &ast.CommentGroup{List: []*ast.Comment{{Text: text}}}
}

// TestObservedDecoratorNamedTypesFailClosed is the regression for named
// parameter types: a secret-named local alias over int64 records its digit
// count, never its value; an imported numeric type the generator cannot
// resolve records its type only and is not even stored.
func TestObservedDecoratorNamedTypesFailClosed(t *testing.T) {
	_, dir := writeModule(t, map[string]string{
		"contract.go": `package orders

import (
	"context"

	"example.com/shop/internal/units"
)

// OTPCode is a one-time-password counter.
type OTPCode int64

type Service interface {
	Redeem(ctx context.Context, otpID OTPCode, fee units.Cents, retries OTPCode) error
}
`,
	})
	got := renderOrders(t, dir, Options{})
	for _, want := range []string{
		`forgeMiddlewareServiceRedeemArgs{otpID, retries}`,
		`r.IntDigits("otpID", int64(a.otpID))`,
		`r.Type("fee", "units.Cents")`,
		`r.Int("retries", int64(a.retries))`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("decorator missing %q\n---\n%s", want, got)
		}
	}
	if strings.Contains(got, "a.fee") || strings.Contains(got, `r.Int("otpID"`) {
		t.Errorf("a named type leaked a value:\n%s", got)
	}
}

// TestObservedDecoratorSecretNamedPlainNumbers is the regression for a
// secret-named number that is not ID-shaped: `pin`, `cvv`,
// `securityCode` and `tan` record their digit count and never reach a value recorder;
// a bool under a secret name, a plain number and a quantity (`maxTokens`)
// keep their value, and a string named like a quantity records its length.
func TestObservedDecoratorSecretNamedPlainNumbers(t *testing.T) {
	_, dir := writeModule(t, map[string]string{
		"contract.go": `package orders

import "context"

type Service interface {
	Confirm(ctx context.Context, pin int, cvv uint16, securityCode int64, tan float32, mfaEnabled bool, spinner int, maxTokens int, tokenLimit string) error
}
`,
	})
	got := renderOrders(t, dir, Options{})
	for _, want := range []string{
		`r.IntDigits("pin", int64(a.pin))`,
		`r.UintDigits("cvv", uint64(a.cvv))`,
		`r.IntDigits("securityCode", a.securityCode)`,
		`r.FloatDigits("tan", float64(a.tan))`,
		`r.Bool("mfaEnabled", a.mfaEnabled)`,
		`r.Int("spinner", int64(a.spinner))`,
		`r.Int("maxTokens", int64(a.maxTokens))`,
		`r.Len("tokenLimit", len(a.tokenLimit))`,
	} {
		if !strings.Contains(got, want) {
			t.Errorf("decorator missing %q\n---\n%s", want, got)
		}
	}
	for _, leak := range []string{`r.Int("pin"`, `r.Uint("cvv"`, `r.Int("securityCode"`} {
		if strings.Contains(got, leak) {
			t.Errorf("a secret-named number is recorded by value (%s):\n%s", leak, got)
		}
	}
}
