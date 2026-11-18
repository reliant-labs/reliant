package crud

import (
	"context"
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"
)

// cause_test.go — an internal error whose cause exists nowhere.
//
// mapRepoErr used to build its client error from scratch and DROP the
// repository error on the floor. The client got "list user failed", which is
// correct, and the operator got the same eight words — the *pgconn.PgError,
// its SQLSTATE and the failing relation were gone. Observed on a running
// scaffold with the table renamed away: HTTP 500 on every request, and
// grepping the entire process log for the SQLSTATE returned zero lines.
//
// The file's comment claimed the driver text was "recorded on the span", and
// it is, but OTEL_EXPORTER_OTLP_ENDPOINT defaults to empty — so in the DEFAULT
// configuration the span is not a second copy of anything.

// TestMapRepoErr_PreservesDriverCause is the guard: whatever the client is
// told, the driver error must remain reachable server-side.
func TestMapRepoErr_PreservesDriverCause(t *testing.T) {
	t.Parallel()
	pgErr := &pgconn.PgError{
		Code:    "42P01",
		Message: `relation "items" does not exist`,
	}

	err := mapRepoErr("list", "item", pgErr)

	// The wire half is unchanged: safe summary, no SQL.
	cerr := new(connect.Error)
	if !errors.As(err, &cerr) || cerr.Code() != connect.CodeInternal {
		t.Fatalf("want a CodeInternal connect error, got %v", err)
	}
	if !strings.Contains(cerr.Message(), "list item failed") {
		t.Errorf("client message lost its summary: %q", cerr.Message())
	}
	if strings.Contains(cerr.Message(), "42P01") || strings.Contains(cerr.Message(), "relation") {
		t.Errorf("driver text leaked to the client: %q", cerr.Message())
	}

	// The half that was missing: the diagnostic still exists.
	cause := svcerr.Cause(err)
	if cause == nil {
		t.Fatal("mapRepoErr discarded the repository error — an internal failure with no cause anywhere")
	}
	if !strings.Contains(cause.Error(), "42P01") || !strings.Contains(cause.Error(), "items") {
		t.Errorf("cause does not carry the driver diagnostic: %q", cause.Error())
	}

	// And it is still the ORIGINAL error, so a caller can inspect it.
	var typed *pgconn.PgError
	if !errors.As(err, &typed) {
		t.Fatal("errors.As can no longer reach the *pgconn.PgError")
	}
	if typed.Code != "42P01" {
		t.Errorf("recovered SQLSTATE = %q, want 42P01", typed.Code)
	}
}

// TestMapRepoErr_ClassifiedViolationsUnaffected guards the boundary: the
// SQLSTATEs that map to a SPECIFIC client-actionable code are the application
// telling the client something true, and they must keep doing that.
//
// It also draws the line the redaction rule actually cares about. The driver
// error below MENTIONS a constraint in its free-text Message and reports no
// ConstraintName, which is the shape a proxy or a non-postgres engine
// produces. The mapping reads the STRUCTURED field and never the prose, so
// nothing crosses here — while the same constraint reported structurally
// does cross (constraint_identity_test.go). Prose is the driver's; the
// identifier is the application's.
func TestMapRepoErr_ClassifiedViolationsUnaffected(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name     string
		sqlstate string
		wantCode connect.Code
	}{
		{"unique violation", "23505", connect.CodeAlreadyExists},
		{"foreign key violation", "23503", connect.CodeFailedPrecondition},
		{"check violation", "23514", connect.CodeInvalidArgument},
		{"not null violation", "23502", connect.CodeInvalidArgument},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			pgErr := &pgconn.PgError{Code: tc.sqlstate, Message: "constraint items_name_key violated"}
			err := mapRepoErr("create", "item", pgErr)

			cerr := new(connect.Error)
			if !errors.As(err, &cerr) || cerr.Code() != tc.wantCode {
				t.Fatalf("code = %v, want %v", svcerr.Code(err), tc.wantCode)
			}
			if strings.Contains(cerr.Message(), "items_name_key") {
				t.Errorf("the mapping read the driver's prose instead of its structured fields: %q", cerr.Message())
			}
		})
	}
}

// TestHandleList_InternalFailureCarriesCause proves it end to end through a
// real op, not just the mapping function.
func TestHandleList_InternalFailureCarriesCause(t *testing.T) {
	t.Parallel()
	pgErr := &pgconn.PgError{Code: "42P01", Message: `relation "users" does not exist`}

	h := HandleList(ListOp[listReq, listResp, *user]{
		EntityLower:   "user",
		PkColumnName:  "id",
		HasPagination: true,
		PageToken:     func(r *listReq) string { return r.PageToken },
		PageSize:      func(r *listReq) int { return r.PageSize },
		Query: func(context.Context, []orm.QueryOption) ([]*user, error) {
			return nil, pgErr
		},
		EntityID: func(u *user) string { return u.ID },
		Pack: func([]*user, string, int64) (*listResp, error) {
			return &listResp{}, nil
		},
	})

	_, err := h(context.Background(), connect.NewRequest(&listReq{}))
	if err == nil {
		t.Fatal("expected an error")
	}
	if cause := svcerr.Cause(err); cause == nil || !strings.Contains(cause.Error(), "42P01") {
		t.Fatalf("the failure reached the handler boundary with no recoverable cause: %v", err)
	}
}
