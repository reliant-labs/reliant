package dbbrowse

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"github.com/jackc/pgx/v5"
	"github.com/jackc/pgx/v5/pgconn"

	"github.com/reliant-labs/forge/pkg/dbquery"
)

// failingBeginner fails BeginTx with err.
type failingBeginner struct{ err error }

func (b failingBeginner) BeginTx(context.Context, pgx.TxOptions) (pgx.Tx, error) {
	return nil, b.err
}

func TestDescribe_FailuresAreDbqueryErrors(t *testing.T) {
	t.Parallel()
	pg := func(code, msg string) error {
		return fmt.Errorf("wrapped: %w", &pgconn.PgError{Code: code, Message: msg})
	}
	for _, tc := range []struct {
		name string
		err  error
		kind dbquery.Kind
	}{
		{"statement timeout", pg("57014", "canceling statement due to statement timeout"), dbquery.KindTimeout},
		{"permission", pg("42501", "permission denied for schema app"), dbquery.KindPermission},
		{"connection failure", pg("08006", "connection failure"), dbquery.KindUnavailable},
		{"sql", pg("42P01", "relation does not exist"), dbquery.KindSQL},
		{"deadline", context.DeadlineExceeded, dbquery.KindTimeout},
		{"canceled", context.Canceled, dbquery.KindCanceled},
		{"dial", errors.New("dial tcp: connection refused"), dbquery.KindUnavailable},
	} {
		_, err := Describe(context.Background(), failingBeginner{tc.err})
		var de *dbquery.Error
		if !errors.As(err, &de) || de.Kind != tc.kind {
			t.Errorf("%s: err = %v (kind %q), want kind %q", tc.name, err, dbquery.KindOf(err), tc.kind)
			continue
		}
		if !errors.Is(err, tc.err) {
			t.Errorf("%s: the cause is not in the chain", tc.name)
		}
		if !strings.HasPrefix(de.Message, "reading the database's tables: ") {
			t.Errorf("%s: message = %q", tc.name, de.Message)
		}
	}

	// An *Error already in the chain lends its Kind, stays in the chain, and
	// is not written to.
	orig := &dbquery.Error{Kind: dbquery.KindAudit, Message: "x"}
	_, err := Describe(context.Background(), failingBeginner{fmt.Errorf("wrapped: %w", orig)})
	if dbquery.KindOf(err) != dbquery.KindAudit || !errors.Is(err, orig) || orig.Message != "x" {
		t.Errorf("err = %v (kind %q), original message %q", err, dbquery.KindOf(err), orig.Message)
	}
}
