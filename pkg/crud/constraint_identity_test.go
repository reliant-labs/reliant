package crud

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"connectrpc.com/connect"
	"github.com/jackc/pgx/v5/pgconn"
	"github.com/lib/pq"
	"github.com/uptrace/bun"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/reliant-labs/forge/pkg/svcerr"
)

// constraint_identity_test.go — a 400 an admin cannot act on.
//
// A CHECK violation used to reach the client as
//
//	create prescription: a field value violates a constraint
//
// with reason=constraint_violated. A UI can route on that; a person cannot
// fix it, because nothing in it says WHICH field. The prescription form has
// eight, and the answer — `prescriptions_expires_after_issued_check` — was
// sitting in the driver error, discarded by a rule written for a different
// problem.
//
// The rule ("the message stays SQL-free") is right about driver
// DIAGNOSTICS: `duplicate key value violates unique constraint ...`,
// `Key (sku)=(abc) already exists.`, the SQLSTATE, the connection string —
// none of that is a message anyone on this side of the wire wrote, and it
// leaks values and topology. But the constraint NAME is not a diagnostic.
// It is an identifier the application typed into its own migration, it is
// the only thing pointing at the offending field, and postgres reports it
// as a STRUCTURED field of its own (ConstraintName / SchemaName-style), not
// as prose inside the message. The same holds for the column name postgres
// reports on a NOT NULL violation, which has no constraint to name.
//
// So: the authored identity crosses, the driver's free text does not.

// namedViolation is one class-23 SQLSTATE plus the schema identity postgres
// reports for it, and the substring that identity must produce in the
// client's message.
type namedViolation struct {
	name     string
	sqlstate string
	want     string // the authored identity that must reach the client
}

var namedViolations = []namedViolation{
	{"check", "23514", "gadgets_category_check"},
	{"unique", "23505", "gadgets_sku_key"},
	{"foreign key", "23503", "gadgets_owner_id_fkey"},
	// A NOT NULL violation carries no constraint name — postgres reports
	// the COLUMN instead, which is the same answer to the same question.
	{"not null", "23502", "category"},
}

// driverLeaks is everything that must still NOT cross: the SQLSTATE, the
// driver's own prose, and the row values it quotes back.
var driverLeaks = []string{
	"23514", "23505", "23503", "23502",
	"duplicate key value violates unique constraint",
	"Key (sku)=(abc) already exists.",
	"dsn=postgres://",
}

func TestMapRepoErr_NamesTheAuthoredConstraint(t *testing.T) {
	t.Parallel()
	drivers := []struct {
		name string
		mk   func(v namedViolation) error
	}{
		{"pgx", func(v namedViolation) error {
			e := &pgconn.PgError{
				Code:    v.sqlstate,
				Message: `duplicate key value violates unique constraint "gadgets_sku_key" dsn=postgres://app:s3cr3t@db/prod`,
				Detail:  "Key (sku)=(abc) already exists.",
			}
			if v.sqlstate == "23502" {
				e.ColumnName = v.want
			} else {
				e.ConstraintName = v.want
			}
			return e
		}},
		{"pq", func(v namedViolation) error {
			e := &pq.Error{
				Code:    pq.ErrorCode(v.sqlstate),
				Message: `duplicate key value violates unique constraint "gadgets_sku_key" dsn=postgres://app:s3cr3t@db/prod`,
				Detail:  "Key (sku)=(abc) already exists.",
			}
			if v.sqlstate == "23502" {
				e.Column = v.want
			} else {
				e.Constraint = v.want
			}
			return e
		}},
	}

	for _, drv := range drivers {
		for _, v := range namedViolations {
			t.Run(drv.name+"/"+v.name, func(t *testing.T) {
				t.Parallel()
				err := mapRepoErr("create", "gadget", fmt.Errorf("create gadgets: %w", drv.mk(v)))
				cerr := new(connect.Error)
				if !errors.As(err, &cerr) {
					t.Fatalf("want a connect.Error, got %T: %v", err, err)
				}
				msg := cerr.Message()
				if !strings.Contains(msg, v.want) {
					t.Errorf("client cannot tell which field failed — message %q names no schema object (want %q)", msg, v.want)
				}
				for _, leak := range driverLeaks {
					if strings.Contains(msg, leak) {
						t.Errorf("driver text %q leaked to the client: %q", leak, msg)
					}
				}
			})
		}
	}
}

// TestMapRepoErr_UnnamedViolationStillReads: a driver that reports no
// constraint or column (some proxies, and every non-postgres path) must
// still produce a sentence, not a dangling fragment.
func TestMapRepoErr_UnnamedViolationStillReads(t *testing.T) {
	t.Parallel()
	for _, v := range namedViolations {
		t.Run(v.name, func(t *testing.T) {
			t.Parallel()
			err := mapRepoErr("create", "gadget", &pgconn.PgError{Code: v.sqlstate})
			cerr := new(connect.Error)
			if !errors.As(err, &cerr) {
				t.Fatalf("want a connect.Error, got %T", err)
			}
			msg := cerr.Message()
			if !strings.HasPrefix(msg, "create gadget: ") {
				t.Errorf("message lost its envelope: %q", msg)
			}
			if strings.Contains(msg, "()") || strings.HasSuffix(msg, " ") {
				t.Errorf("message has a dangling identity phrase: %q", msg)
			}
		})
	}
}

// prescription is the smallest entity carrying a NAMED, multi-column CHECK
// — the shape that produced the finding, and the one no single column name
// could have identified.
type prescription struct {
	bun.BaseModel `bun:"table:prescriptions,alias:prescriptions"`

	ID        string    `bun:"id,pk"`
	IssuedAt  time.Time `bun:"issued_at,notnull"`
	ExpiresAt time.Time `bun:"expires_at,notnull"`
}

const createPrescriptionProcedure = "/crud.test.v1.PrescriptionService/CreatePrescription"

// TestCreate_CheckViolationNamesTheConstraintOnTheWire is the whole finding,
// end to end and over HTTP: a real postgres table with a named CHECK, the
// real crud.Repo write path, a real connect handler, and a raw client
// reading the raw response — i.e. exactly what an admin's browser sees when
// the form is submitted with an expiry before the issue date.
func TestCreate_CheckViolationNamesTheConstraintOnTheWire(t *testing.T) {
	db := newRepoTestDB(t)
	ddl := `CREATE TABLE prescriptions (
		id          TEXT PRIMARY KEY,
		issued_at   TIMESTAMPTZ NOT NULL,
		expires_at  TIMESTAMPTZ NOT NULL,
		CONSTRAINT prescriptions_expires_after_issued_check CHECK (expires_at > issued_at)
	)`
	if _, err := db.Bun().ExecContext(context.Background(), ddl); err != nil {
		t.Fatalf("create table: %v", err)
	}
	repo := NewRepo[prescription]()

	mux := http.NewServeMux()
	mux.Handle(createPrescriptionProcedure, connect.NewUnaryHandler(
		createPrescriptionProcedure,
		HandleCreate(CreateOp[structpb.Struct, structpb.Struct, *prescription]{
			EntityLower: "prescription",
			Entity: func(_ context.Context, req *structpb.Struct) (*prescription, error) {
				issued, err := time.Parse(time.RFC3339, req.GetFields()["issued_at"].GetStringValue())
				if err != nil {
					return nil, err
				}
				expires, err := time.Parse(time.RFC3339, req.GetFields()["expires_at"].GetStringValue())
				if err != nil {
					return nil, err
				}
				return &prescription{IssuedAt: issued, ExpiresAt: expires}, nil
			},
			Persist: func(ctx context.Context, e *prescription) error { return repo.Create(ctx, db, e) },
			Pack: func(e *prescription) (*structpb.Struct, error) {
				return structpb.NewStruct(map[string]any{"id": e.ID})
			},
		}),
	))
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	req, err := http.NewRequest(http.MethodPost, srv.URL+createPrescriptionProcedure,
		strings.NewReader(`{"issued_at":"2026-07-01T00:00:00Z","expires_at":"2026-06-01T00:00:00Z"}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	body := string(raw)

	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("status = %d, want 400 (body %s)", resp.StatusCode, body)
	}
	if got := resp.Header.Get(svcerr.ReasonHeader); got != ReasonConstraintViolated {
		t.Errorf("%s = %q, want %q (body %s)", svcerr.ReasonHeader, got, ReasonConstraintViolated, body)
	}
	if !strings.Contains(body, "prescriptions_expires_after_issued_check") {
		t.Errorf("the admin gets a 400 naming no field: %s", body)
	}
	for _, leak := range []string{"23514", "SQLSTATE", "INSERT INTO"} {
		if strings.Contains(body, leak) {
			t.Errorf("driver diagnostics leaked to the client (%q): %s", leak, body)
		}
	}
	t.Logf("wire: %d %s: %s  body=%s", resp.StatusCode, svcerr.ReasonHeader,
		resp.Header.Get(svcerr.ReasonHeader), body)
}
