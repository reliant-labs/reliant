package forgeconv

import (
	"go/parser"
	"path/filepath"
	"strings"
	"testing"
)

// writeHandlerFixture writes files (slash paths relative to the project)
// into a fresh project and returns its root.
func writeHandlerFixture(t *testing.T, files map[string]string) string {
	t.Helper()
	tmp := t.TempDir()
	for rel, body := range files {
		path := filepath.Join(tmp, filepath.FromSlash(rel))
		must(t, mkdirAll(filepath.Dir(path)))
		must(t, writeFile(path, body))
	}
	return tmp
}

const handlerConnectErrorV2Fixture = `package thing

import (
	"context"
	"fmt"

	"connectrpc.com/connect/v2"
)

func fixed() error {
	return connect.NewError(connect.CodeInvalidArgument, "id is required")
}

func fixedWithCause(err error) error {
	return connect.NewError(connect.CodeInternal, "push failed").WithCause(err)
}

func fixedFormat(name string) error {
	return connect.Errorf(connect.CodeAlreadyExists, "name %q is taken", name)
}

func leaked(err error) error {
	return connect.NewError(connect.CodeInternal, err.Error())
}

func leakedFormat(ctx context.Context, err error) error {
	return connect.Errorf(connect.CodeNotFound, "lookup: %v", err)
}

func leakedSprintf(err error) error {
	return connect.NewError(connect.CodeUnavailable, fmt.Sprintf("dial: %v", err))
}

func computed(msg string) error {
	return connect.NewError(connect.CodeFailedPrecondition, msg)
}

func empty() error {
	return connect.NewError(connect.CodeUnauthenticated, "")
}
`

// connect-go v2's NewError takes a message string and serializes it
// verbatim. A fixed message is the hand mapping svcerr replaces; a message
// built from an error's text is the leak; a `.WithCause(err)` is local
// only. No v2 finding points at `forge fix connect-errors`, which rewrites
// v1 code only.
func TestHandlerConnectError_ConnectV2(t *testing.T) {
	root := writeHandlerFixture(t, map[string]string{"internal/handlers/thing/thing.go": handlerConnectErrorV2Fixture})
	res, err := LintHandlerErrorMapping(root)
	if err != nil {
		t.Fatalf("LintHandlerErrorMapping: %v", err)
	}
	got := findingsForRule(res.Findings, handlerConnectErrorRule)
	want := []struct {
		line                 int
		message, remediation string
	}{
		{11, "connect.NewError with a fixed message", "svcerr.InvalidArgument(\"<message>\")"},
		{15, "connect.NewError with a fixed message", "svcerr.Internal(\"<message>\")"},
		{19, "connect.Errorf with a fixed message", "svcerr.AlreadyExists("},
		{23, "publishes it to the client", "svcerr.WrapOr(err, svcerr.Internal("},
		{27, "publishes it to the client", "svcerr.WrapOr(err, svcerr.NotFound("},
		{31, "publishes it to the client", "svcerr.WrapOr(err, svcerr.Unavailable("},
		{35, "computed message", "svcerr.FailedPrecondition(msg)"},
		{39, "empty message", "svcerr.ErrUnauthenticated"},
	}
	if len(got) != len(want) {
		t.Fatalf("expected %d findings, got %d:\n%s", len(want), len(got), res.FormatText())
	}
	for i, w := range want {
		f := got[i]
		if f.Line != w.line || !strings.Contains(f.Message, w.message) || !strings.Contains(f.Remediation, w.remediation) {
			t.Errorf("finding %d = line %d %q → %q; want line %d containing %q → %q", i, f.Line, f.Message, f.Remediation, w.line, w.message, w.remediation)
		}
		if f.Severity != SeverityWarning {
			t.Errorf("finding %d severity = %s, want warning", i, f.Severity)
		}
		if strings.Contains(f.Remediation, "`forge fix connect-errors` rewrites this site") {
			t.Errorf("finding %d promises a codemod that skips v2 code: %s", i, f.Remediation)
		}
	}
}

// An aliased v2 import is resolved through the file's imports; a v1 import
// is read exactly as before (a v1 Errorf does not exist and is not a site).
func TestHandlerConnectError_ConnectV2AliasAndV1Unchanged(t *testing.T) {
	root := writeHandlerFixture(t, map[string]string{
		"internal/handlers/a/a.go": "package a\n\nimport cv2 \"connectrpc.com/connect/v2\"\n\n" +
			"func f(err error) error { return cv2.NewError(cv2.CodeInternal, err.Error()) }\n",
		"internal/handlers/b/b.go": "package b\n\nimport (\n\t\"errors\"\n\n\t\"connectrpc.com/connect\"\n)\n\n" +
			"func f() error { return connect.NewError(connect.CodeInternal, errors.New(\"x\")) }\n",
	})
	res, err := LintHandlerErrorMapping(root)
	if err != nil {
		t.Fatal(err)
	}
	got := findingsForRule(res.Findings, handlerConnectErrorRule)
	if len(got) != 2 {
		t.Fatalf("expected 2 findings, got %d:\n%s", len(got), res.FormatText())
	}
	if !strings.Contains(got[0].Message, "cv2.NewError with an error's text") {
		t.Errorf("aliased v2 site: %s", got[0].Message)
	}
	if got[1].Message != "connect.NewError with a fixed message: the hand-built connect error svcerr replaces" ||
		!strings.Contains(got[1].Remediation, "`forge fix connect-errors` rewrites this site") {
		t.Errorf("v1 site changed: %q → %q", got[1].Message, got[1].Remediation)
	}
}

func TestConnectV2MessageShape(t *testing.T) {
	cases := map[string]connectErrorArg{
		`"id is required"`:                   argFixedMessage,
		"`raw`":                              argFixedMessage,
		`""`:                                 argNil,
		`fmt.Sprintf("x %s", req.GetName())`: argFixedMessage,
		`fmt.Sprintf("x: %v", err)`:          argErrorValue,
		`err.Error()`:                        argErrorValue,
		`dbErr.Error()`:                      argErrorValue,
		`"push: " + err.Error()`:             argErrorValue,
		`msg`:                                argComputedMessage,
		`describe(req)`:                      argComputedMessage,
	}
	for src, want := range cases {
		e, err := parser.ParseExpr(src)
		if err != nil {
			t.Fatalf("parse %s: %v", src, err)
		}
		if got := connectV2MessageShape(e); got != want {
			t.Errorf("connectV2MessageShape(%s) = %d, want %d", src, got, want)
		}
	}
}

// The mapper rule sees a v2 mapper: errors.Is arms returning v2's
// NewError / Errorf, under an alias as well as under `connect`.
func TestNoHandlerErrorMapping_ConnectV2Mapper(t *testing.T) {
	const mapper = `package thing

import (
	"errors"

	cv2 "connectrpc.com/connect/v2"
)

var (
	errMissing = errors.New("missing")
	errTaken   = errors.New("taken")
)

func translate(err error) error {
	switch {
	case errors.Is(err, errMissing):
		return cv2.NewError(cv2.CodeNotFound, "not found")
	case errors.Is(err, errTaken):
		return cv2.Errorf(cv2.CodeAlreadyExists, "taken")
	}
	return err
}
`
	root := writeHandlerFixture(t, map[string]string{"internal/handlers/thing/thing.go": mapper})
	res, err := LintHandlerErrorMapping(root)
	if err != nil {
		t.Fatal(err)
	}
	got := findingsForRule(res.Findings, "forgeconv-no-handler-error-mapping")
	if len(got) != 1 || !strings.Contains(got[0].Message, `"translate" maps 2 sentinel(s)`) {
		t.Fatalf("expected the v2 mapper flagged, got:\n%s", res.FormatText())
	}
}

const unwrappedV2Handler = `package orders

import (
	"context"

	ordersv1 "example.com/app/gen/orders/v1"
	"example.com/app/gen/orders/v1/ordersv1connect"
)

type Service struct{ deps Deps }

// unary: the request message is the method's own.
func (s *Service) ShipOrder(ctx context.Context, req *ordersv1.ShipOrderRequest) (*ordersv1.ShipOrderResponse, error) {
	order, err := s.deps.Lifecycle.Ship(ctx, req.GetId())
	if err != nil {
		return nil, err
	}
	return &ordersv1.ShipOrderResponse{Order: order}, nil
}

// server-streaming: its own generated stream type.
func (s *Service) WatchOrders(ctx context.Context, req *ordersv1.WatchOrdersRequest, stream ordersv1connect.OrderServiceWatchOrdersServerStream) error {
	_, err := s.deps.Lifecycle.Watch(ctx, req.GetId())
	return err
}

// a helper taking SOME proto message is not on the boundary.
func (s *Service) load(ctx context.Context, req *ordersv1.ShipOrderRequest) (*ordersv1.Order, error) {
	order, err := s.deps.Lifecycle.Load(ctx, req.GetId())
	return order, err
}
`

// A connect-go v2 handler names no connect type, so its shape is read
// where v2 is known to be in use: the project's go.mod. A v1 project is
// read exactly as before, and the same file there yields nothing.
func TestUnwrappedDomainError_ConnectV2Handlers(t *testing.T) {
	for _, tt := range []struct {
		name  string
		gomod string
		want  int
	}{
		{"v2 project", "module example.com/app\n\ngo 1.26\n\nrequire connectrpc.com/connect/v2 v2.0.2\n", 2},
		{"v1 project", "module example.com/app\n\ngo 1.26\n\nrequire (\n\tconnectrpc.com/connect v1.21.1\n\tconnectrpc.com/connect/v2 v2.0.2 // indirect\n)\n", 0},
	} {
		t.Run(tt.name, func(t *testing.T) {
			root := writeHandlerFixture(t, map[string]string{
				"go.mod":                             tt.gomod,
				"internal/handlers/orders/orders.go": unwrappedV2Handler,
			})
			res, err := LintHandlerErrorMapping(root)
			if err != nil {
				t.Fatal(err)
			}
			got := findingsForRule(res.Findings, unwrappedDomainErrorRule)
			if len(got) != tt.want {
				t.Fatalf("expected %d findings, got %d:\n%s", tt.want, len(got), res.FormatText())
			}
			for _, f := range got {
				if strings.Contains(f.Message, "load returns") {
					t.Errorf("a helper was read as a handler: %s", f.Message)
				}
			}
		})
	}
}

// A file that imports connect-go v2 is read as v2 whatever go.mod says,
// and a v2 constructor's result is a wire error, not a raw one.
func TestUnwrappedDomainError_ConnectV2ImportAndConstructors(t *testing.T) {
	src := strings.Replace(unwrappedV2Handler, "import (\n", "import (\n\tconnect \"connectrpc.com/connect/v2\"\n", 1) + `
func (s *Service) CancelOrder(ctx context.Context, req *ordersv1.CancelOrderRequest) (*ordersv1.CancelOrderResponse, error) {
	_, err := s.deps.Lifecycle.Cancel(ctx, req.GetId())
	if err != nil {
		err = connect.NewError(connect.CodeInternal, "cancel failed").WithCause(err)
		return nil, err
	}
	return &ordersv1.CancelOrderResponse{}, nil
}

var _ = connect.CodeInternal
`
	root := writeHandlerFixture(t, map[string]string{"internal/handlers/orders/orders.go": src})
	res, err := LintHandlerErrorMapping(root)
	if err != nil {
		t.Fatal(err)
	}
	got := findingsForRule(res.Findings, unwrappedDomainErrorRule)
	if len(got) != 2 {
		t.Fatalf("expected ShipOrder and WatchOrders only, got %d:\n%s", len(got), res.FormatText())
	}
}
