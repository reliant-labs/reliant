package crud

import (
	"context"
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"
)

// A client update_mask naming a column the wire declares read-only
// (forge:read-only / forge:computed) is refused before anything runs: no
// entity projection, no write. The repository would have accepted it — the
// masked path is how a custom RPC writes these columns — so the refusal has
// to happen on the op, which is the only layer that knows a CLIENT is asking.
func TestHandleUpdate_ReadOnlyMaskPath_Refused(t *testing.T) {
	var fullCalled bool
	var maskedFields []string
	op := maskedUpdateOp(&fullCalled, &maskedFields, nil)
	op.ReadOnly = []string{"email"}
	entityBuilt := false
	build := op.Entity
	op.Entity = func(ctx context.Context, r *updateReq) (*user, error) {
		entityBuilt = true
		return build(ctx, r)
	}

	_, err := HandleUpdate(op)(context.Background(), connect.NewRequest(&updateReq{
		User: &user{ID: "u1", Name: "B", Email: "takeover@x"},
		Mask: []string{"name", "email"},
	}))

	cerr := new(connect.Error)
	if !errors.As(err, &cerr) || cerr.Code() != connect.CodeInvalidArgument {
		t.Fatalf("want InvalidArgument for a read-only mask path, got %v", err)
	}
	if got := cerr.Meta().Get(svcerr.ReasonHeader); got != ReasonUnknownField {
		t.Errorf("reason = %q, want %q — the same reason a forge:version path gets", got, ReasonUnknownField)
	}
	if !strings.Contains(cerr.Message(), `"email"`) || !strings.Contains(cerr.Message(), "read-only") {
		t.Errorf("message should name the path and say why, got %q", cerr.Message())
	}
	if maskedFields != nil || fullCalled {
		t.Errorf("nothing may be written when a path is refused (masked=%v full=%v)", maskedFields, fullCalled)
	}
	if entityBuilt {
		t.Error("the entity hook ran for a request already known to be refused")
	}
}

// Only the named paths are checked: a mask of editable columns on an op that
// declares read-only ones goes through untouched.
func TestHandleUpdate_ReadOnly_EditablePathsStillWrite(t *testing.T) {
	var fullCalled bool
	var maskedFields []string
	op := maskedUpdateOp(&fullCalled, &maskedFields, nil)
	op.ReadOnly = []string{"email"}

	if _, err := HandleUpdate(op)(context.Background(), connect.NewRequest(&updateReq{
		User: &user{ID: "u1", Name: "B"},
		Mask: []string{"name"},
	})); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(maskedFields) != 1 || maskedFields[0] != "name" {
		t.Errorf("PersistMasked fields = %v, want [name]", maskedFields)
	}
}

// A full replace (no mask, or "*") is not refused: the request carries the
// whole entity by design (AIP-134), read-only fields included. Keeping those
// values out of the row is the generated Persist's job (Preserve), which this
// test pins by asserting HandleUpdate still dispatches to it.
func TestHandleUpdate_ReadOnly_FullReplaceDispatchesToPersist(t *testing.T) {
	for _, mask := range [][]string{nil, {"*"}} {
		var fullCalled bool
		var maskedFields []string
		op := maskedUpdateOp(&fullCalled, &maskedFields, nil)
		op.ReadOnly = []string{"email"}
		if _, err := HandleUpdate(op)(context.Background(), connect.NewRequest(&updateReq{
			User: &user{ID: "u1", Email: "ignored@x"},
			Mask: mask,
		})); err != nil {
			t.Fatalf("mask %v: unexpected err: %v", mask, err)
		}
		if !fullCalled {
			t.Errorf("mask %v: full replace must still reach Persist", mask)
		}
	}
}

// An op with no ReadOnly declaration behaves exactly as before.
func TestHandleUpdate_NoReadOnly_MaskUnchanged(t *testing.T) {
	var maskedFields []string
	var fullCalled bool
	h := HandleUpdate(UpdateOp[updateReq, updateResp, *user]{
		EntityLower:    "user",
		EntityFieldLow: "user",
		Entity:         func(_ context.Context, r *updateReq) (*user, error) { return r.User, entityOrRequired(r.User) },
		Mask:           func(r *updateReq) []string { return r.Mask },
		Persist: func(context.Context, *user, ...orm.QueryOption) error {
			fullCalled = true
			return nil
		},
		PersistMasked: func(_ context.Context, _ *user, fields []string, _ ...orm.QueryOption) error {
			maskedFields = fields
			return nil
		},
		Pack: func(u *user) (*updateResp, error) { return &updateResp{User: u}, nil },
	})
	if _, err := h(context.Background(), connect.NewRequest(&updateReq{
		User: &user{ID: "u1"},
		Mask: []string{"email"},
	})); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if fullCalled || len(maskedFields) != 1 || maskedFields[0] != "email" {
		t.Errorf("masked dispatch changed: full=%v masked=%v", fullCalled, maskedFields)
	}
}
