package svcerr_test

import (
	"errors"
	"fmt"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// redaction_test.go — what a client is allowed to read.
//
// connect.Error.Message() is verbatim err.Error(), so handing a driver
// error to connect.NewError publishes it. Following the documented handler
// idiom, a planted DSN came back to an UNAUTHENTICATED caller in a 500:
//
//	{"code":"internal","message":"ERROR: relation \"accounts\" does not exist
//	 (SQLSTATE 42P01) dsn=postgres://app:s3cr3t@db.internal:5432/prod"}
//
// The code was right; the message was never sanitized. These tests pin both
// halves of the rule: redact the FALLBACK internal error, and leave every
// deliberately-chosen message alone.

// pgShapedError stands in for a *pgconn.PgError: an error type the
// application did not define, whose text it did not write.
type pgShapedError struct {
	sqlstate string
	detail   string
}

func (e *pgShapedError) Error() string {
	return fmt.Sprintf("ERROR: %s (SQLSTATE %s)", e.detail, e.sqlstate)
}

const plantedSecret = "dsn=postgres://app:s3cr3t@db.internal:5432/prod"

func TestWrap_RedactsUnrecognizedInternalError(t *testing.T) {
	t.Parallel()
	raw := &pgShapedError{
		sqlstate: "42P01",
		detail:   `relation "accounts" does not exist ` + plantedSecret,
	}

	ce := svcerr.ToConnect(raw)
	if ce == nil {
		t.Fatal("ToConnect returned nil for a non-nil error")
	}
	if ce.Code() != connect.CodeInternal {
		t.Fatalf("code = %v, want %v", ce.Code(), connect.CodeInternal)
	}
	if ce.Message() != svcerr.InternalMessage {
		t.Errorf("client message = %q, want the fixed %q", ce.Message(), svcerr.InternalMessage)
	}
	if strings.Contains(ce.Error(), plantedSecret) {
		t.Errorf("the connection string reached the wire: %q", ce.Error())
	}
	if strings.Contains(ce.Error(), "SQLSTATE") {
		t.Errorf("driver diagnostics reached the wire: %q", ce.Error())
	}

	// Redacted for the client, intact for the server.
	cause := svcerr.Cause(ce)
	if cause == nil {
		t.Fatal("svcerr.Cause returned nil — the diagnostic exists nowhere")
	}
	if !strings.Contains(cause.Error(), plantedSecret) {
		t.Errorf("cause lost the original detail: %q", cause.Error())
	}
	var typed *pgShapedError
	if !errors.As(ce, &typed) {
		t.Error("errors.As can no longer reach the driver error — redaction must not break the chain")
	}
}

func TestWrap_PreservesDeliberateMessages(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name    string
		err     error
		code    connect.Code
		wantMsg string
	}{
		{"not found", svcerr.NotFound("user"), connect.CodeNotFound, "user not found"},
		{"invalid argument", svcerr.InvalidArgument("name is required"), connect.CodeInvalidArgument, "name is required"},
		{"failed precondition", svcerr.FailedPrecondition("no billing account"), connect.CodeFailedPrecondition, "no billing account"},
		// Internal is the interesting one: the CODE is the same as the
		// redacted fallback, but the application wrote this string.
		{"deliberate internal", svcerr.Internal("create order failed"), connect.CodeInternal, "create order failed"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			ce := svcerr.ToConnect(tc.err)
			if ce.Code() != tc.code {
				t.Errorf("code = %v, want %v", ce.Code(), tc.code)
			}
			if ce.Message() != tc.wantMsg {
				t.Errorf("message = %q, want %q — a message the application chose must survive", ce.Message(), tc.wantMsg)
			}
		})
	}
}

func TestWithCause_SafeMessageOutDiagnosticIn(t *testing.T) {
	t.Parallel()
	raw := &pgShapedError{sqlstate: "42P01", detail: `relation "items" does not exist ` + plantedSecret}
	err := svcerr.WithCause(svcerr.Internal("get item failed"), raw)

	ce := svcerr.ToConnect(err)
	if ce.Code() != connect.CodeInternal {
		t.Errorf("code = %v, want CodeInternal — WithCause must not change the sentinel's code", ce.Code())
	}
	if ce.Message() != "get item failed" {
		t.Errorf("message = %q, want the safe summary", ce.Message())
	}
	if strings.Contains(ce.Error(), plantedSecret) {
		t.Errorf("cause leaked to the wire: %q", ce.Error())
	}

	cause := svcerr.Cause(ce)
	if cause == nil || !strings.Contains(cause.Error(), plantedSecret) {
		t.Errorf("Cause must return the diagnostic, got %v", cause)
	}
	if !errors.Is(err, svcerr.ErrInternal) {
		t.Error("WithCause broke errors.Is on the sentinel")
	}
	var typed *pgShapedError
	if !errors.As(err, &typed) {
		t.Error("WithCause broke errors.As on the underlying driver error")
	}
}

func TestCause_NilWhenNothingWithheld(t *testing.T) {
	t.Parallel()
	if got := svcerr.Cause(nil); got != nil {
		t.Errorf("Cause(nil) = %v, want nil", got)
	}
	if got := svcerr.Cause(svcerr.NotFound("user")); got != nil {
		t.Errorf("Cause on a plain sentinel = %v, want nil (nothing was withheld)", got)
	}
}

// TestWrap_ExplicitConnectErrorStillPassesThrough guards the boundary of the
// rule: an error the handler built itself is the handler's decision, and
// svcerr must not second-guess it. mapPackErr and hand-written
// connect.NewError call sites depend on this.
func TestWrap_ExplicitConnectErrorStillPassesThrough(t *testing.T) {
	t.Parallel()
	original := connect.NewError(connect.CodeInternal, errors.New("corrupt enum value \"FOO\" for column status"))
	ce := svcerr.ToConnect(original)
	if ce.Message() != "corrupt enum value \"FOO\" for column status" {
		t.Errorf("an explicit *connect.Error must pass through unchanged, got %q", ce.Message())
	}
}
