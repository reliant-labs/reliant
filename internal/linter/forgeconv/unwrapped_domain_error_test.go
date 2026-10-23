package forgeconv

import (
	"fmt"
	"path/filepath"
	"strings"
	"testing"
)

// TestUnwrappedDomainError_FiresOnTheReproduction is the assertion that
// earns the rule: the exact peptides-rw1 file that shipped four
// `return nil, err` sites and made every order-lifecycle RPC answer
// 500 unknown. All four must fire, at error severity, or `forge lint`
// stays green over an app no client can use.
func TestUnwrappedDomainError_FiresOnTheReproduction(t *testing.T) {
	res, err := LintHandlerErrorMapping(filepath.Join("testdata", "handlers_unwrapped"))
	if err != nil {
		t.Fatalf("LintHandlerErrorMapping: %v", err)
	}
	got := findingsForRule(res.Findings, unwrappedDomainErrorRule)
	if len(got) != 4 {
		t.Fatalf("expected 4 findings (the four order-lifecycle RPCs), got %d:\n%s",
			len(got), res.FormatText())
	}

	for _, f := range got {
		// Every finding must land in the hand-written orders file. A
		// finding in the prescriptions file would mean the rule is
		// demanding a second wrap of an already-wrapped error.
		if !strings.Contains(filepath.ToSlash(f.File), "handlers_order_lifecycle.go") {
			t.Errorf("finding in %s; only handlers_order_lifecycle.go is defective", f.File)
		}
		if f.Severity != SeverityError {
			t.Errorf("severity = %s, want error — a warning is what this defect already survived", f.Severity)
		}
		if !strings.Contains(f.Remediation, "svcerr.Wrap") ||
			!strings.Contains(f.Remediation, "forge/pkg/svcerr") {
			t.Errorf("remediation must name svcerr.Wrap and its import path; got: %s", f.Remediation)
		}
		// The message has to name the wire consequence, not just the
		// shape — the reader's question is "so what?".
		if !strings.Contains(f.Message, "x-forge-error-reason") {
			t.Errorf("message should name the lost reason header; got: %s", f.Message)
		}
	}

	// Each finding must name its own handler and the collaborator call
	// the error came from, so the message alone is actionable.
	report := res.FormatText()
	for _, rpc := range []string{"SubmitOrder", "ApproveOrder", "ShipOrder", "CancelOrder"} {
		if !strings.Contains(report, rpc) {
			t.Errorf("expected a finding naming %s; got:\n%s", rpc, report)
		}
		if !strings.Contains(report, "s.deps.Lifecycle."+rpc+"(...)") {
			t.Errorf("expected the finding for %s to name its collaborator call; got:\n%s", rpc, report)
		}
	}
}

// TestUnwrappedDomainError_SilentOnTheCorrectShapes pins the whole
// false-positive surface in one place. Each entry was a real firing of a
// wider cut of the rule against control-plane / cp-forge /
// peptides-dogfood-r2, or a shape forge itself generates.
func TestUnwrappedDomainError_SilentOnTheCorrectShapes(t *testing.T) {
	res, err := LintHandlerErrorMapping(filepath.Join("testdata", "handlers_unwrapped"))
	if err != nil {
		t.Fatalf("LintHandlerErrorMapping: %v", err)
	}
	for _, f := range findingsForRule(res.Findings, unwrappedDomainErrorRule) {
		base := filepath.Base(f.File)
		if base != "handlers_order_lifecycle.go" {
			t.Errorf("%s:%d fired on a correct shape: %s", f.File, f.Line, f.Message)
		}
	}
}

// TestUnwrappedDomainError_ShapeMatrix drives the classifier directly,
// one shape per row, so a regression names the shape it broke rather than
// a count.
func TestUnwrappedDomainError_ShapeMatrix(t *testing.T) {
	tests := []struct {
		name string
		body string
		want int
	}{
		{
			name: "collaborator error returned bare",
			body: `res, err := s.deps.Orders.Ship(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	_ = res`,
			want: 1,
		},
		{
			name: "collaborator held one level deep",
			body: `res, err := s.orders.Ship(ctx, req.Msg.GetId())
	if err != nil {
		return nil, err
	}
	_ = res`,
			want: 1,
		},
		{
			name: "wrapped inline by forge's helper",
			body: `res, err := s.deps.Orders.Ship(ctx, req.Msg.GetId())
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	_ = res`,
			want: 0,
		},
		{
			name: "wrapped inline by the project's own helper",
			body: `res, err := s.deps.Orders.Ship(ctx, req.Msg.GetId())
	if err != nil {
		return nil, onWire(err)
	}
	_ = res`,
			want: 0,
		},
		{
			name: "wrapped by reassignment",
			body: `res, err := s.deps.Orders.Ship(ctx, req.Msg.GetId())
	if err != nil {
		err = onWire(err)
		return nil, err
	}
	_ = res`,
			want: 0,
		},
		{
			name: "already a connect error, built here",
			body: `ce := connect.NewError(connect.CodeInvalidArgument, errBad)
	return nil, ce`,
			want: 0,
		},
		{
			name: "single-result guard through a package",
			body: `if err := middleware.VerifyAuth(ctx, "admin"); err != nil {
		return nil, err
	}`,
			want: 0,
		},
		{
			name: "multi-result guard through a package",
			body: `user, err := middleware.RequireAdminOrOperator(ctx)
	if err != nil {
		return nil, err
	}
	_ = user`,
			want: 0,
		},
		{
			name: "same-package converter",
			body: `m, err := orderToProto(row)
	if err != nil {
		return nil, err
	}
	_ = m`,
			want: 0,
		},
		{
			name: "method on the handler's own receiver",
			body: `v, err := s.load(ctx)
	if err != nil {
		return nil, err
	}
	_ = v`,
			want: 0,
		},
		{
			name: "raw once, wrapped once — flags only the raw return",
			body: `a, err := s.deps.Orders.Ship(ctx, "1")
	if err != nil {
		return nil, svcerr.Wrap(err)
	}
	b, err2 := s.deps.Orders.Cancel(ctx, "1")
	if err2 != nil {
		return nil, err2
	}
	_, _ = a, b`,
			want: 1,
		},
		{
			name: "collaborator error inside a closure does not cross the boundary",
			body: `g := func() error {
		_, err := s.deps.Orders.Ship(ctx, "1")
		return err
	}
	if err := g(); err != nil {
		return nil, svcerr.Wrap(err)
	}`,
			want: 0,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			dir := filepath.Join(t.TempDir(), "handlers", "admin")
			must(t, mkdirAll(dir))
			src := fmt.Sprintf(`package admin

import (
	"context"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "example.com/app/gen/services/admin/v1"
	"example.com/app/pkg/middleware"
)

func (s *Service) DoThing(
	ctx context.Context,
	req *connect.Request[pb.DoThingRequest],
) (*connect.Response[pb.DoThingResponse], error) {
	%s
	return connect.NewResponse(&pb.DoThingResponse{}), nil
}
`, tt.body)
			must(t, writeFile(filepath.Join(dir, "handlers.go"), src))

			res, err := LintHandlerErrorMapping(filepath.Dir(filepath.Dir(dir)))
			if err != nil {
				t.Fatalf("LintHandlerErrorMapping: %v", err)
			}
			got := findingsForRule(res.Findings, unwrappedDomainErrorRule)
			if len(got) != tt.want {
				t.Fatalf("got %d finding(s), want %d:\n%s", len(got), tt.want, res.FormatText())
			}
		})
	}
}

// TestUnwrappedDomainError_BornStubIsClean guards the thing the whole
// exercise turns on: forge's own scaffold must never trip its own lint.
// The born handler stub returns svcerr.Wrap(svcerr.ScaffoldStub(...)) for
// every RPC shape, unary and streaming alike.
func TestUnwrappedDomainError_BornStubIsClean(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "handlers", "admin")
	must(t, mkdirAll(dir))
	must(t, writeFile(filepath.Join(dir, "handlers.go"), `package admin

import (
	"context"

	"connectrpc.com/connect"
	"github.com/reliant-labs/forge/pkg/svcerr"

	pb "example.com/app/gen/services/admin/v1"
)

func (s *Service) DoThing(
	ctx context.Context,
	req *connect.Request[pb.DoThingRequest],
) (*connect.Response[pb.DoThingResponse], error) {
	return nil, svcerr.Wrap(svcerr.ScaffoldStub("DoThing"))
}

func (s *Service) StreamThings(
	ctx context.Context,
	req *connect.Request[pb.StreamThingsRequest],
	stream *connect.ServerStream[pb.StreamThingsResponse],
) error {
	return svcerr.Wrap(svcerr.ScaffoldStub("StreamThings"))
}
`))
	res, err := LintHandlerErrorMapping(filepath.Dir(filepath.Dir(dir)))
	if err != nil {
		t.Fatalf("LintHandlerErrorMapping: %v", err)
	}
	if got := findingsForRule(res.Findings, unwrappedDomainErrorRule); len(got) != 0 {
		t.Fatalf("forge's own born stub must not trip the rule, got %d:\n%s", len(got), res.FormatText())
	}
}
