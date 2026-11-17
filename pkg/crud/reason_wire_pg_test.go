package crud

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"connectrpc.com/connect"
	"github.com/uptrace/bun"
	"google.golang.org/protobuf/types/known/structpb"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"
)

// This test exists because the contract was documented but never emitted.
//
// web-runtime/src/errors.ts tells every frontend author: read
// `error.reason` (the x-forge-error-reason metadata), NEVER the message
// text, because message text is display-only. A live scaffolded app was
// then curl'd on a duplicate-email POST and answered 409 with NO
// x-forge-error-reason header — so the only machine-readable discriminator
// available was the prose, and the caller ended up shipping
// `text.includes("already exists")`. A documented contract that is not
// emitted is worse than no contract: it points at a painted-on door.
//
// Everything below the handler is real: real embedded postgres, a real
// UNIQUE index, a real *pgconn.PgError, the real crud.Repo write path, a
// real connect.NewUnaryHandler, and a raw net/http client that reads the
// raw response headers — i.e. exactly what the curl saw. The assertion is
// on the WIRE, not on an in-process connect.Error, because the whole
// question is whether the reason survives the trip through
// connectUnaryHandlerConn.Close into the HTTP response headers.

// account is the smallest entity that can produce a duplicate-email
// conflict: a string PK and a UNIQUE, NOT NULL email.
type account struct {
	bun.BaseModel `bun:"table:accounts,alias:accounts"`

	ID    string `bun:"id,pk"`
	Email string `bun:"email,notnull"`
}

const createAccountProcedure = "/crud.test.v1.AccountService/CreateAccount"

// accountCreateServer mounts crud.HandleCreate behind a real Connect unary
// handler, wired to a real postgres table — the same construction a
// generated handlers_crud_gen.go performs, minus the proto types (a
// structpb.Struct stands in for the request/response messages so the test
// needs no generated code).
func accountCreateServer(t *testing.T) *httptest.Server {
	t.Helper()
	db := newRepoTestDB(t)
	if _, err := db.Bun().ExecContext(context.Background(),
		`CREATE TABLE accounts (id TEXT PRIMARY KEY, email TEXT NOT NULL UNIQUE)`); err != nil {
		t.Fatalf("create accounts table: %v", err)
	}
	repo := NewRepo[account]()

	handler := connect.NewUnaryHandler(
		createAccountProcedure,
		HandleCreate(CreateOp[structpb.Struct, structpb.Struct, *account]{
			EntityLower: "account",
			Entity: func(_ context.Context, req *structpb.Struct) (*account, error) {
				return &account{Email: req.GetFields()["email"].GetStringValue()}, nil
			},
			Persist: func(ctx context.Context, entity *account) error {
				return repo.Create(ctx, db, entity)
			},
			Pack: func(entity *account) (*structpb.Struct, error) {
				return structpb.NewStruct(map[string]any{"id": entity.ID, "email": entity.Email})
			},
		}),
	)
	mux := http.NewServeMux()
	mux.Handle(createAccountProcedure, handler)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return srv
}

// postCreateAccount issues the Connect-protocol JSON unary call a browser
// (or a curl) would issue, and returns the raw status, headers and body.
func postCreateAccount(t *testing.T, srv *httptest.Server, email string) (int, http.Header, string) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, srv.URL+createAccountProcedure,
		strings.NewReader(`{"email":"`+email+`"}`))
	if err != nil {
		t.Fatalf("new request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := srv.Client().Do(req)
	if err != nil {
		t.Fatalf("post: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatalf("read body: %v", err)
	}
	return resp.StatusCode, resp.Header, string(body)
}

// TestCreate_DuplicateCarriesReasonOnTheWire is the regression that the
// missing header earned. It performs the exact sequence the dogfood curl
// performed — create an account, create it again with the same email — and
// asserts the conflict response carries a machine-readable reason.
func TestCreate_DuplicateCarriesReasonOnTheWire(t *testing.T) {
	if testing.Short() {
		t.Skip("boots embedded postgres; skipped under -short")
	}
	srv := accountCreateServer(t)

	if status, _, body := postCreateAccount(t, srv, "dup@example.com"); status != http.StatusOK {
		t.Fatalf("first create: status %d, want 200 (body %s)", status, body)
	}

	status, hdr, body := postCreateAccount(t, srv, "dup@example.com")
	if status != http.StatusConflict {
		t.Fatalf("duplicate create: status %d, want 409 (body %s)", status, body)
	}
	got := hdr.Get(svcerr.ReasonHeader)
	if got == "" {
		t.Fatalf("duplicate create returned NO %s header — the frontend has nothing to key off "+
			"but the message prose. headers=%v body=%s", svcerr.ReasonHeader, hdr, body)
	}
	if got != ReasonDuplicate {
		t.Fatalf("%s = %q, want %q", svcerr.ReasonHeader, got, ReasonDuplicate)
	}
	t.Logf("wire: %d %s: %s  body=%s", status, svcerr.ReasonHeader, got, body)

	// The reason must be enough on its own to ROUTE: reaching the same
	// verdict needs no SQL, no SQLSTATE and no key value, and none of those
	// appear. (The violated constraint's name does appear, in the message —
	// that is display copy for a person, never the routing key.)
	for _, leak := range []string{"23505", "duplicate key", "Key (email)"} {
		if strings.Contains(body, leak) {
			t.Errorf("conflict body leaks driver text %q: %s", leak, body)
		}
	}
}

// TestCreate_NotFoundAndInternalCarryReasons pins the OTHER half of the
// contract: `reason` is TOTAL. A frontend that switches on error.reason
// must never hit a null, or it is back to sniffing prose for the cases the
// switch missed. Both a classified miss and an unclassified failure are
// checked on the wire.
func TestCreate_ReasonIsTotalOnTheWire(t *testing.T) {
	if testing.Short() {
		t.Skip("boots embedded postgres; skipped under -short")
	}
	db := newRepoTestDB(t)

	cases := []struct {
		name       string
		persist    func(ctx context.Context, entity *account) error
		wantStatus int
		wantReason string
	}{
		{
			name:       "classified not-found",
			persist:    func(context.Context, *account) error { return orm.ErrNoRows },
			wantStatus: http.StatusNotFound,
			wantReason: ReasonNotFound,
		},
		{
			name: "unclassified driver failure",
			persist: func(ctx context.Context, entity *account) error {
				// A real postgres error with no SQLSTATE mapping of its own:
				// the table does not exist (42P01).
				_, err := db.Bun().ExecContext(ctx, `INSERT INTO ghosts (id) VALUES ('x')`)
				return err
			},
			wantStatus: http.StatusInternalServerError,
			wantReason: ReasonInternal,
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			handler := connect.NewUnaryHandler(
				createAccountProcedure,
				HandleCreate(CreateOp[structpb.Struct, structpb.Struct, *account]{
					EntityLower: "account",
					Entity:      func(context.Context, *structpb.Struct) (*account, error) { return &account{}, nil },
					Persist:     tc.persist,
					Pack: func(*account) (*structpb.Struct, error) {
						return structpb.NewStruct(map[string]any{})
					},
				}),
			)
			mux := http.NewServeMux()
			mux.Handle(createAccountProcedure, handler)
			srv := httptest.NewServer(mux)
			t.Cleanup(srv.Close)

			status, hdr, body := postCreateAccount(t, srv, "x@example.com")
			if status != tc.wantStatus {
				t.Fatalf("status %d, want %d (body %s)", status, tc.wantStatus, body)
			}
			if got := hdr.Get(svcerr.ReasonHeader); got != tc.wantReason {
				t.Fatalf("%s = %q, want %q (headers %v, body %s)",
					svcerr.ReasonHeader, got, tc.wantReason, hdr, body)
			}
			// An unclassified failure must still say nothing about the database.
			if tc.wantStatus == http.StatusInternalServerError {
				var wire struct {
					Message string `json:"message"`
				}
				if err := json.Unmarshal([]byte(body), &wire); err != nil {
					t.Fatalf("unmarshal wire error: %v (%s)", err, body)
				}
				for _, leak := range []string{"42P01", "ghosts", "relation"} {
					if strings.Contains(wire.Message, leak) {
						t.Errorf("internal message leaks %q: %q", leak, wire.Message)
					}
				}
			}
		})
	}
}
