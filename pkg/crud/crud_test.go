package crud

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"
)

// Test fixture types: a tiny "User" entity and per-RPC req/resp shapes
// that mirror what the proto gen would produce.

type user struct {
	ID    string
	Name  string
	Email string
}

type createReq struct {
	Name  string
	Email string
}
type createResp struct{ User *user }

type getReq struct{ ID string }
type getResp struct{ User *user }

type updateReq struct {
	User *user
	Mask []string // stands in for the proto's update_mask paths
}
type updateResp struct{ User *user }

type deleteReq struct{ ID string }
type deleteResp struct{ ID string }

type listReq struct {
	PageSize   int
	PageToken  string
	Search     *string
	OrderBy    string
	Descending bool
}
type listResp struct {
	Users         []*user
	NextPageToken string
	TotalCount    int64
}

// fakeRepo is the test stand-in for the per-project db.* helpers.
type fakeRepo struct {
	store      map[string]*user
	createErr  error
	getErr     error
	listErr    error
	updateErr  error
	deleteErr  error
	listResult []*user
	queryOpts  []orm.QueryOption
}

func newRepo() *fakeRepo {
	return &fakeRepo{store: map[string]*user{}}
}

// --- HandleCreate -----------------------------------------------------

func TestHandleCreate_HappyPath(t *testing.T) {
	repo := newRepo()
	h := HandleCreate(CreateOp[createReq, createResp, *user]{
		EntityLower: "user",
		Entity: func(_ context.Context, r *createReq) (*user, error) {
			return &user{Name: r.Name, Email: r.Email}, nil
		},
		Persist: func(ctx context.Context, e *user) error {
			if repo.createErr != nil {
				return repo.createErr
			}
			e.ID = "u1"
			repo.store[e.ID] = e
			return nil
		},
		Pack: func(e *user) (*createResp, error) { return &createResp{User: e}, nil },
	})
	resp, err := h(context.Background(), connect.NewRequest(&createReq{Name: "Ada", Email: "a@x"}))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if resp.Msg.User.Name != "Ada" || resp.Msg.User.ID != "u1" {
		t.Fatalf("bad resp: %+v", resp.Msg.User)
	}
}

func TestHandleCreate_RepoError_WrappedAsInternal(t *testing.T) {
	repo := newRepo()
	repo.createErr = errors.New("db down")
	h := HandleCreate(CreateOp[createReq, createResp, *user]{
		EntityLower: "user",
		Entity:      func(_ context.Context, r *createReq) (*user, error) { return &user{}, nil },
		Persist:     func(context.Context, *user) error { return repo.createErr },
		Pack:        func(*user) (*createResp, error) { return &createResp{}, nil },
	})
	_, err := h(context.Background(), connect.NewRequest(&createReq{}))
	cerr := new(connect.Error)
	if !errors.As(err, &cerr) || cerr.Code() != connect.CodeInternal {
		t.Fatalf("want Internal, got %v", err)
	}
	if !strings.Contains(cerr.Message(), "create user failed") {
		t.Fatalf("error envelope wording changed: %q", cerr.Message())
	}
	// Repo error text must never reach the client.
	if strings.Contains(cerr.Message(), "db down") {
		t.Fatalf("repo error leaked into client message: %q", cerr.Message())
	}
}

// --- HandleGet --------------------------------------------------------

func TestHandleGet_HappyPath(t *testing.T) {
	want := &user{ID: "u1", Name: "Ada"}
	h := HandleGet(GetOp[getReq, getResp, *user]{
		EntityLower: "user",
		ID:          func(r *getReq) string { return r.ID },
		Fetch:       func(context.Context, string, ...orm.QueryOption) (*user, error) { return want, nil },
		Pack:        func(u *user) (*getResp, error) { return &getResp{User: u}, nil },
	})
	resp, err := h(context.Background(), connect.NewRequest(&getReq{ID: "u1"}))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if resp.Msg.User != want {
		t.Fatal("unexpected entity returned")
	}
}

func TestHandleGet_NotFound(t *testing.T) {
	h := HandleGet(GetOp[getReq, getResp, *user]{
		EntityLower: "user",
		ID:          func(r *getReq) string { return r.ID },
		Fetch: func(context.Context, string, ...orm.QueryOption) (*user, error) {
			// Repos signal a missing row via orm.ErrNoRows (possibly wrapped).
			return nil, fmt.Errorf("get users by id: %w", orm.ErrNoRows)
		},
		Pack: func(u *user) (*getResp, error) { return &getResp{User: u}, nil },
	})
	_, err := h(context.Background(), connect.NewRequest(&getReq{ID: "nope"}))
	cerr := new(connect.Error)
	if !errors.As(err, &cerr) || cerr.Code() != connect.CodeNotFound {
		t.Fatalf("want NotFound, got %v", err)
	}
	// Clean svcerr envelope: entity name + not-found, no repo text.
	if !strings.Contains(cerr.Message(), "user not found") {
		t.Fatalf("error envelope wording changed: %q", cerr.Message())
	}
	if strings.Contains(cerr.Message(), "get users by id") {
		t.Fatalf("repo error leaked into client message: %q", cerr.Message())
	}
}

func TestHandleGet_SvcerrNotFound(t *testing.T) {
	// A repository that already classified the miss via svcerr maps to
	// NotFound too.
	h := HandleGet(GetOp[getReq, getResp, *user]{
		EntityLower: "user",
		ID:          func(r *getReq) string { return r.ID },
		Fetch: func(context.Context, string, ...orm.QueryOption) (*user, error) {
			return nil, svcerr.NotFound("user")
		},
		Pack: func(u *user) (*getResp, error) { return &getResp{User: u}, nil },
	})
	_, err := h(context.Background(), connect.NewRequest(&getReq{ID: "nope"}))
	cerr := new(connect.Error)
	if !errors.As(err, &cerr) || cerr.Code() != connect.CodeNotFound {
		t.Fatalf("want NotFound, got %v", err)
	}
}

func TestHandleGet_ArbitraryRepoError_Internal(t *testing.T) {
	// A non-ErrNoRows repo error is INTERNAL, not NotFound, and its SQL
	// text must never cross the wire.
	h := HandleGet(GetOp[getReq, getResp, *user]{
		EntityLower: "user",
		ID:          func(r *getReq) string { return r.ID },
		Fetch: func(context.Context, string, ...orm.QueryOption) (*user, error) {
			return nil, errors.New("boom: SELECT * FROM x")
		},
		Pack: func(u *user) (*getResp, error) { return &getResp{User: u}, nil },
	})
	_, err := h(context.Background(), connect.NewRequest(&getReq{ID: "u1"}))
	cerr := new(connect.Error)
	if !errors.As(err, &cerr) || cerr.Code() != connect.CodeInternal {
		t.Fatalf("want Internal, got %v", err)
	}
	if !strings.Contains(cerr.Message(), "get user failed") {
		t.Fatalf("error envelope wording changed: %q", cerr.Message())
	}
	if strings.Contains(cerr.Message(), "SELECT") {
		t.Fatalf("SQL text leaked into client message: %q", cerr.Message())
	}
}

// --- HandleUpdate -----------------------------------------------------

func TestHandleUpdate_HappyPath(t *testing.T) {
	h := HandleUpdate(UpdateOp[updateReq, updateResp, *user]{
		EntityLower:    "user",
		EntityFieldLow: "user",
		Entity: func(_ context.Context, r *updateReq) (*user, error) {
			if r.User == nil {
				return nil, ErrEntityRequired
			}
			return r.User, nil
		},
		Persist: func(context.Context, *user, ...orm.QueryOption) error { return nil },
		Pack:    func(u *user) (*updateResp, error) { return &updateResp{User: u}, nil },
	})
	resp, err := h(context.Background(), connect.NewRequest(&updateReq{User: &user{ID: "u1", Name: "B"}}))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if resp.Msg.User.Name != "B" {
		t.Fatal("entity not propagated through Pack")
	}
}

func TestHandleUpdate_NilEntity_InvalidArgument(t *testing.T) {
	h := HandleUpdate(UpdateOp[updateReq, updateResp, *user]{
		EntityLower:    "user",
		EntityFieldLow: "user",
		Entity:         func(_ context.Context, r *updateReq) (*user, error) { return r.User, entityOrRequired(r.User) },
		Persist:        func(context.Context, *user, ...orm.QueryOption) error { return nil },
		Pack:           func(*user) (*updateResp, error) { return &updateResp{}, nil },
	})
	_, err := h(context.Background(), connect.NewRequest(&updateReq{}))
	cerr := new(connect.Error)
	if !errors.As(err, &cerr) || cerr.Code() != connect.CodeInvalidArgument {
		t.Fatalf("want InvalidArgument, got %v", err)
	}
	// Locked behavioural fingerprint: same wording as legacy generator.
	want := "update user: user is required"
	if !strings.Contains(cerr.Message(), want) {
		t.Fatalf("legacy message format changed.\nwant substring: %q\ngot: %q", want, cerr.Message())
	}
}

// --- HandleUpdate: AIP-134 update_mask dispatch -----------------------

// maskedUpdateOp builds an UpdateOp with both persistence hooks
// instrumented, so each test can assert exactly which path ran.
func maskedUpdateOp(fullCalled *bool, maskedFields *[]string, maskedErr error) UpdateOp[updateReq, updateResp, *user] {
	return UpdateOp[updateReq, updateResp, *user]{
		EntityLower:    "user",
		EntityFieldLow: "user",
		Entity: func(_ context.Context, r *updateReq) (*user, error) {
			return r.User, entityOrRequired(r.User)
		},
		Mask: func(r *updateReq) []string { return r.Mask },
		Persist: func(context.Context, *user, ...orm.QueryOption) error {
			*fullCalled = true
			return nil
		},
		PersistMasked: func(_ context.Context, _ *user, fields []string, _ ...orm.QueryOption) error {
			*maskedFields = append([]string{}, fields...)
			return maskedErr
		},
		Pack: func(u *user) (*updateResp, error) { return &updateResp{User: u}, nil },
	}
}

func TestHandleUpdate_MaskedPaths_DispatchToPersistMasked(t *testing.T) {
	var fullCalled bool
	var maskedFields []string
	h := HandleUpdate(maskedUpdateOp(&fullCalled, &maskedFields, nil))
	resp, err := h(context.Background(), connect.NewRequest(&updateReq{
		User: &user{ID: "u1", Name: "B"},
		Mask: []string{"name"},
	}))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if fullCalled {
		t.Error("full-replace Persist ran despite a concrete update_mask — that is the data-loss bug")
	}
	if len(maskedFields) != 1 || maskedFields[0] != "name" {
		t.Errorf("PersistMasked fields = %v, want [name]", maskedFields)
	}
	if resp.Msg.User == nil || resp.Msg.User.Name != "B" {
		t.Error("entity not propagated through Pack")
	}
}

func TestHandleUpdate_EmptyMask_FullReplace(t *testing.T) {
	var fullCalled bool
	var maskedFields []string
	h := HandleUpdate(maskedUpdateOp(&fullCalled, &maskedFields, nil))
	if _, err := h(context.Background(), connect.NewRequest(&updateReq{
		User: &user{ID: "u1"},
	})); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !fullCalled {
		t.Error("empty mask must mean documented full-object replace via Persist")
	}
	if maskedFields != nil {
		t.Errorf("PersistMasked must not run on empty mask, got fields %v", maskedFields)
	}
}

func TestHandleUpdate_StarMask_FullReplace(t *testing.T) {
	var fullCalled bool
	var maskedFields []string
	h := HandleUpdate(maskedUpdateOp(&fullCalled, &maskedFields, nil))
	if _, err := h(context.Background(), connect.NewRequest(&updateReq{
		User: &user{ID: "u1"},
		Mask: []string{"*"},
	})); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !fullCalled {
		t.Error(`mask ["*"] must mean full-object replace via Persist`)
	}
	if maskedFields != nil {
		t.Errorf("PersistMasked must not run on a * mask, got fields %v", maskedFields)
	}
}

func TestHandleUpdate_MaskWithoutPersistMasked_Internal(t *testing.T) {
	// Mask wired but PersistMasked missing is a generator wiring bug. It
	// must surface as Internal — silently falling back to full replace is
	// exactly the data-loss the mask exists to prevent.
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
		Pack: func(u *user) (*updateResp, error) { return &updateResp{User: u}, nil },
	})
	_, err := h(context.Background(), connect.NewRequest(&updateReq{
		User: &user{ID: "u1"},
		Mask: []string{"name"},
	}))
	cerr := new(connect.Error)
	if !errors.As(err, &cerr) || cerr.Code() != connect.CodeInternal {
		t.Fatalf("want Internal for missing PersistMasked wiring, got %v", err)
	}
	if fullCalled {
		t.Error("must not silently full-replace when masked persistence is unwired")
	}
}

func TestHandleUpdate_UnknownMaskPath_InvalidArgument(t *testing.T) {
	var fullCalled bool
	var maskedFields []string
	repoErr := fmt.Errorf("update users: %w", &orm.UnknownFieldError{Field: "no_such_column"})
	h := HandleUpdate(maskedUpdateOp(&fullCalled, &maskedFields, repoErr))
	_, err := h(context.Background(), connect.NewRequest(&updateReq{
		User: &user{ID: "u1"},
		Mask: []string{"no_such_column"},
	}))
	cerr := new(connect.Error)
	if !errors.As(err, &cerr) || cerr.Code() != connect.CodeInvalidArgument {
		t.Fatalf("want InvalidArgument for unknown mask path, got %v", err)
	}
	if !strings.Contains(cerr.Message(), "no_such_column") {
		t.Errorf("client error should name the offending path, got %q", cerr.Message())
	}
	for _, leak := range []string{"SELECT", "UPDATE", "sql:"} {
		if strings.Contains(cerr.Message(), leak) {
			t.Errorf("client error leaks SQL-ish text (%q): %q", leak, cerr.Message())
		}
	}
}

func TestHandleUpdate_NilMaskHook_LegacyFullReplace(t *testing.T) {
	// No Mask hook (proto without update_mask): behavior is unchanged —
	// Persist runs even when the request struct happens to carry paths.
	var fullCalled bool
	h := HandleUpdate(UpdateOp[updateReq, updateResp, *user]{
		EntityLower:    "user",
		EntityFieldLow: "user",
		Entity:         func(_ context.Context, r *updateReq) (*user, error) { return r.User, entityOrRequired(r.User) },
		Persist: func(context.Context, *user, ...orm.QueryOption) error {
			fullCalled = true
			return nil
		},
		Pack: func(u *user) (*updateResp, error) { return &updateResp{User: u}, nil },
	})
	if _, err := h(context.Background(), connect.NewRequest(&updateReq{
		User: &user{ID: "u1"},
		Mask: []string{"name"},
	})); err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if !fullCalled {
		t.Error("nil Mask hook must preserve the legacy full-replace path")
	}
}

// --- HandleDelete -----------------------------------------------------

func TestHandleDelete_DefaultZeroResp(t *testing.T) {
	h := HandleDelete(DeleteOp[deleteReq, deleteResp]{
		EntityLower: "user",
		ID:          func(r *deleteReq) string { return r.ID },
		Persist:     func(context.Context, string, ...orm.QueryOption) error { return nil },
	})
	resp, err := h(context.Background(), connect.NewRequest(&deleteReq{ID: "u1"}))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if resp.Msg == nil || resp.Msg.ID != "" {
		t.Fatalf("want zero-valued resp, got %+v", resp.Msg)
	}
}

func TestHandleDelete_PackOverride(t *testing.T) {
	h := HandleDelete(DeleteOp[deleteReq, deleteResp]{
		EntityLower: "user",
		ID:          func(r *deleteReq) string { return r.ID },
		Persist:     func(context.Context, string, ...orm.QueryOption) error { return nil },
		Pack:        func() *deleteResp { return &deleteResp{ID: "echoed"} },
	})
	resp, _ := h(context.Background(), connect.NewRequest(&deleteReq{ID: "u1"}))
	if resp.Msg.ID != "echoed" {
		t.Fatalf("Pack override not used")
	}
}

func TestHandleDelete_RepoError_WrappedInternal(t *testing.T) {
	h := HandleDelete(DeleteOp[deleteReq, deleteResp]{
		EntityLower: "user",
		ID:          func(r *deleteReq) string { return r.ID },
		Persist:     func(context.Context, string, ...orm.QueryOption) error { return errors.New("fk violation") },
	})
	_, err := h(context.Background(), connect.NewRequest(&deleteReq{ID: "u1"}))
	cerr := new(connect.Error)
	if !errors.As(err, &cerr) || cerr.Code() != connect.CodeInternal {
		t.Fatalf("want Internal, got %v", err)
	}
	if !strings.Contains(cerr.Message(), "delete user failed") {
		t.Fatalf("envelope wording changed: %q", cerr.Message())
	}
	if strings.Contains(cerr.Message(), "fk violation") {
		t.Fatalf("repo error leaked into client message: %q", cerr.Message())
	}
}

// TestHandleDelete_MissingRow_NotFound pins the half of the contract the
// library could not express before the repository read RowsAffected: a
// Delete whose PK matched no row answers exactly as Get does for that same
// id. The 200-with-an-empty-body it used to return was indistinguishable
// from a successful delete, so a typo, an id the caller may not see and a
// real deletion all looked the same on the wire.
func TestHandleDelete_MissingRow_NotFound(t *testing.T) {
	h := HandleDelete(DeleteOp[deleteReq, deleteResp]{
		EntityLower: "user",
		ID:          func(r *deleteReq) string { return r.ID },
		Persist: func(context.Context, string, ...orm.QueryOption) error {
			return fmt.Errorf("delete users: %w", orm.ErrNoRows)
		},
	})
	_, err := h(context.Background(), connect.NewRequest(&deleteReq{ID: "nope"}))
	cerr := new(connect.Error)
	if !errors.As(err, &cerr) || cerr.Code() != connect.CodeNotFound {
		t.Fatalf("want NotFound, got %v", err)
	}
	// Identical envelope to Get's, so a client can reason about one rule.
	if cerr.Message() != "user not found" {
		t.Fatalf("Delete and Get must produce the same not-found envelope; got %q", cerr.Message())
	}
	if strings.Contains(cerr.Message(), "delete users") || strings.Contains(cerr.Message(), "no rows") {
		t.Fatalf("repository/driver text leaked into the client message: %q", cerr.Message())
	}
}

// TestHandleUpdate_MissingRow_NotFound is the same hole on the Update leg:
// HandleUpdate never fetches, so an update of an id that does not exist
// used to write nothing and return 200 echoing the caller's own entity —
// a response describing a row the database does not have.
func TestHandleUpdate_MissingRow_NotFound(t *testing.T) {
	h := HandleUpdate(UpdateOp[updateReq, updateResp, *user]{
		EntityLower:    "user",
		EntityFieldLow: "user",
		Entity:         func(_ context.Context, r *updateReq) (*user, error) { return r.User, entityOrRequired(r.User) },
		Persist: func(context.Context, *user, ...orm.QueryOption) error {
			return fmt.Errorf("update users: %w", orm.ErrNoRows)
		},
		Pack: func(e *user) (*updateResp, error) { return &updateResp{User: e}, nil },
	})
	_, err := h(context.Background(), connect.NewRequest(&updateReq{User: &user{ID: "nope"}}))
	cerr := new(connect.Error)
	if !errors.As(err, &cerr) || cerr.Code() != connect.CodeNotFound {
		t.Fatalf("want NotFound, got %v", err)
	}
}

// --- HandleList -------------------------------------------------------

func TestHandleList_PaginationDefaultsAndTrim(t *testing.T) {
	// Repo returns 51 results; default page size is 50 -> trim, NextPageToken set.
	all := make([]*user, 51)
	for i := range all {
		all[i] = &user{ID: "u" + string(rune('A'+i%26))}
	}
	h := HandleList(ListOp[listReq, listResp, *user]{
		EntityLower:   "user",
		PkColumnName:  "id",
		HasPagination: true,
		PageToken:     func(r *listReq) string { return r.PageToken },
		PageSize:      func(r *listReq) int { return r.PageSize },
		Query: func(ctx context.Context, _ []orm.QueryOption) ([]*user, error) {
			return all, nil
		},
		EntityID: func(u *user) string { return u.ID },
		Pack: func(items []*user, tok string, _ int64) (*listResp, error) {
			return &listResp{Users: items, NextPageToken: tok}, nil
		},
	})
	resp, err := h(context.Background(), connect.NewRequest(&listReq{}))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if len(resp.Msg.Users) != 50 {
		t.Fatalf("expected 50 results after trim, got %d", len(resp.Msg.Users))
	}
	if resp.Msg.NextPageToken == "" {
		t.Fatal("expected non-empty NextPageToken")
	}
	// Cursor round-trip: decode should give back the 50th entity ID.
	got, derr := orm.DecodeCursor(resp.Msg.NextPageToken)
	if derr != nil {
		t.Fatalf("cursor decode: %v", derr)
	}
	if got != all[49].ID {
		t.Fatalf("cursor mismatch: %q vs %q", got, all[49].ID)
	}
}

// TestHandleList_PageSizeClampAndCustomMax checks the page-size clamp by
// examining how many results end up in the response after trim. We seed
// the repo with `wantLimit-1` rows (i.e. the actual page size, with no
// next page) to assert the clamp applied.
func TestHandleList_PageSizeClampAndCustomMax(t *testing.T) {
	cases := []struct {
		name       string
		maxSize    int
		req        int
		wantPageSz int
	}{
		{"zero -> default 50", 0, 0, 50},
		{"over default-max 100 clamped", 0, 999, 100},
		{"custom max applied", 25, 999, 25},
		{"under default min", 0, 1, 1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			// Seed enough rows to overflow any page-size we may compute.
			seed := make([]*user, 200)
			for i := range seed {
				seed[i] = &user{ID: "u" + string(rune('A'+i%26))}
			}
			h := HandleList(ListOp[listReq, listResp, *user]{
				EntityLower:   "user",
				PkColumnName:  "id",
				HasPagination: true,
				MaxPageSize:   tc.maxSize,
				PageToken:     func(r *listReq) string { return "" },
				PageSize:      func(r *listReq) int { return r.PageSize },
				Query: func(ctx context.Context, _ []orm.QueryOption) ([]*user, error) {
					return seed, nil
				},
				EntityID: func(u *user) string { return u.ID },
				Pack: func(items []*user, tok string, _ int64) (*listResp, error) {
					return &listResp{Users: items, NextPageToken: tok}, nil
				},
			})
			resp, err := h(context.Background(), connect.NewRequest(&listReq{PageSize: tc.req}))
			if err != nil {
				t.Fatalf("unexpected err: %v", err)
			}
			if len(resp.Msg.Users) != tc.wantPageSz {
				t.Fatalf("page size = %d, want %d", len(resp.Msg.Users), tc.wantPageSz)
			}
		})
	}
}

func TestHandleList_BadCursor_InvalidArgument(t *testing.T) {
	h := HandleList(ListOp[listReq, listResp, *user]{
		EntityLower:   "user",
		PkColumnName:  "id",
		HasPagination: true,
		PageToken:     func(r *listReq) string { return r.PageToken },
		PageSize:      func(r *listReq) int { return r.PageSize },
		Query: func(ctx context.Context, _ []orm.QueryOption) ([]*user, error) {
			return nil, nil
		},
		EntityID: func(u *user) string { return u.ID },
		Pack:     func(items []*user, tok string, _ int64) (*listResp, error) { return &listResp{}, nil },
	})
	_, err := h(context.Background(), connect.NewRequest(&listReq{PageToken: "%%not-base64"}))
	cerr := new(connect.Error)
	if !errors.As(err, &cerr) || cerr.Code() != connect.CodeInvalidArgument {
		t.Fatalf("want InvalidArgument, got %v", err)
	}
	// Legacy fingerprint preserved.
	if !strings.Contains(cerr.Message(), "invalid page token") {
		t.Fatalf("legacy 'invalid page token' wording lost: %q", cerr.Message())
	}
}

func TestHandleList_NoPagination_NoLimitNoCursor(t *testing.T) {
	var sawOpts int
	h := HandleList(ListOp[listReq, listResp, *user]{
		EntityLower: "user",
		Query: func(ctx context.Context, opts []orm.QueryOption) ([]*user, error) {
			sawOpts = len(opts)
			return []*user{{ID: "x"}}, nil
		},
		Pack: func(items []*user, tok string, _ int64) (*listResp, error) {
			return &listResp{Users: items, NextPageToken: tok}, nil
		},
	})
	resp, err := h(context.Background(), connect.NewRequest(&listReq{}))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if sawOpts != 0 {
		t.Fatalf("HasPagination off => no opts emitted, got %d", sawOpts)
	}
	if resp.Msg.NextPageToken != "" {
		t.Fatalf("NextPageToken should be empty when pagination is off")
	}
}

func TestHandleList_OrderByValidation(t *testing.T) {
	h := HandleList(ListOp[listReq, listResp, *user]{
		EntityLower:   "user",
		PkColumnName:  "id",
		HasPagination: true,
		HasOrderBy:    true,
		PageToken:     func(r *listReq) string { return r.PageToken },
		PageSize:      func(r *listReq) int { return r.PageSize },
		OrderBy:       func(_ context.Context, r *listReq) (string, bool) { return r.OrderBy, r.Descending },
		Query: func(ctx context.Context, _ []orm.QueryOption) ([]*user, error) {
			return nil, nil
		},
		EntityID: func(u *user) string { return u.ID },
		Pack:     func(items []*user, tok string, _ int64) (*listResp, error) { return &listResp{}, nil },
	})
	// "; DROP TABLE" should fail validation.
	_, err := h(context.Background(), connect.NewRequest(&listReq{OrderBy: "name; DROP TABLE x"}))
	cerr := new(connect.Error)
	if !errors.As(err, &cerr) || cerr.Code() != connect.CodeInvalidArgument {
		t.Fatalf("want InvalidArgument from order-by validation, got %v", err)
	}
}

// listOrderByOp builds a List handler with the given Columns allowlist
// for the order-by allowlist tests.
func listOrderByOp(columns []string) func(context.Context, *connect.Request[listReq]) (*connect.Response[listResp], error) {
	return HandleList(ListOp[listReq, listResp, *user]{
		EntityLower:   "user",
		PkColumnName:  "id",
		Columns:       columns,
		HasPagination: true,
		HasOrderBy:    true,
		PageToken:     func(r *listReq) string { return r.PageToken },
		PageSize:      func(r *listReq) int { return r.PageSize },
		OrderBy:       func(_ context.Context, r *listReq) (string, bool) { return r.OrderBy, r.Descending },
		Query: func(ctx context.Context, _ []orm.QueryOption) ([]*user, error) {
			return nil, nil
		},
		EntityID: func(u *user) string { return u.ID },
		Pack:     func(items []*user, tok string, _ int64) (*listResp, error) { return &listResp{}, nil },
	})
}

func TestHandleList_OrderByAllowlist(t *testing.T) {
	columns := []string{"id", "name", "email"}

	t.Run("declared column accepted", func(t *testing.T) {
		h := listOrderByOp(columns)
		if _, err := h(context.Background(), connect.NewRequest(&listReq{OrderBy: "name DESC"})); err != nil {
			t.Fatalf("declared column should pass allowlist: %v", err)
		}
	})

	t.Run("undeclared column rejected", func(t *testing.T) {
		h := listOrderByOp(columns)
		_, err := h(context.Background(), connect.NewRequest(&listReq{OrderBy: "password_hash ASC"}))
		cerr := new(connect.Error)
		if !errors.As(err, &cerr) || cerr.Code() != connect.CodeInvalidArgument {
			t.Fatalf("want InvalidArgument for undeclared order-by column, got %v", err)
		}
	})

	t.Run("nil Columns is shape-only", func(t *testing.T) {
		h := listOrderByOp(nil)
		if _, err := h(context.Background(), connect.NewRequest(&listReq{OrderBy: "password_hash ASC"})); err != nil {
			t.Fatalf("nil Columns should skip allowlist validation: %v", err)
		}
	})
}

// --- HandleList: total_count (Count wiring) ---------------------------

// When a Count hook is wired, HandleList runs it over the SAME filters as the
// list query (but not the page limit/cursor) and hands the total to Pack, so
// the response's total_count is real instead of the pre-fix constant 0.
func TestHandleList_TotalCount_PopulatedFromCount(t *testing.T) {
	var countOpts, listOpts int
	h := HandleList(ListOp[listReq, listResp, *user]{
		EntityLower:   "user",
		PkColumnName:  "id",
		HasPagination: true,
		PageToken:     func(r *listReq) string { return r.PageToken },
		PageSize:      func(r *listReq) int { return r.PageSize },
		Filters: func(_ context.Context, r *listReq) ([]orm.QueryOption, error) {
			// One filter opt — must reach BOTH count and list.
			return []orm.QueryOption{orm.WhereEq("name", "x")}, nil
		},
		Query: func(ctx context.Context, opts []orm.QueryOption) ([]*user, error) {
			listOpts = len(opts)
			return []*user{{ID: "a"}, {ID: "b"}}, nil
		},
		Count: func(ctx context.Context, opts []orm.QueryOption) (int64, error) {
			countOpts = len(opts)
			return 42, nil
		},
		EntityID: func(u *user) string { return u.ID },
		Pack: func(items []*user, tok string, total int64) (*listResp, error) {
			return &listResp{Users: items, NextPageToken: tok, TotalCount: total}, nil
		},
	})
	resp, err := h(context.Background(), connect.NewRequest(&listReq{PageSize: 10}))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if resp.Msg.TotalCount != 42 {
		t.Fatalf("total_count should come from Count: got %d, want 42", resp.Msg.TotalCount)
	}
	// Count sees ONLY the filter opt (no limit/cursor); the list query sees
	// the filter opt PLUS pagination (limit + order) opts — so strictly more.
	if countOpts != 1 {
		t.Fatalf("Count should see exactly the 1 filter opt (no limit/cursor), got %d", countOpts)
	}
	if listOpts <= countOpts {
		t.Fatalf("list query should carry more opts (filter+limit+order) than count (%d), got %d", countOpts, listOpts)
	}
}

// With no Count hook, totalCount is 0 (pre-fix behavior preserved for
// responses that carry no total_count field).
func TestHandleList_TotalCount_ZeroWhenCountUnwired(t *testing.T) {
	h := HandleList(ListOp[listReq, listResp, *user]{
		EntityLower:   "user",
		PkColumnName:  "id",
		HasPagination: true,
		PageToken:     func(r *listReq) string { return r.PageToken },
		PageSize:      func(r *listReq) int { return r.PageSize },
		Query: func(ctx context.Context, _ []orm.QueryOption) ([]*user, error) {
			return []*user{{ID: "a"}}, nil
		},
		EntityID: func(u *user) string { return u.ID },
		Pack: func(items []*user, tok string, total int64) (*listResp, error) {
			return &listResp{Users: items, NextPageToken: tok, TotalCount: total}, nil
		},
	})
	resp, err := h(context.Background(), connect.NewRequest(&listReq{}))
	if err != nil {
		t.Fatalf("unexpected err: %v", err)
	}
	if resp.Msg.TotalCount != 0 {
		t.Fatalf("unwired Count must leave total_count at 0, got %d", resp.Msg.TotalCount)
	}
}

// A Count error surfaces as CodeInternal (SQL-free), like any repo error.
func TestHandleList_TotalCount_CountErrorInternal(t *testing.T) {
	h := HandleList(ListOp[listReq, listResp, *user]{
		EntityLower:   "user",
		PkColumnName:  "id",
		HasPagination: true,
		PageToken:     func(r *listReq) string { return r.PageToken },
		PageSize:      func(r *listReq) int { return r.PageSize },
		Query: func(ctx context.Context, _ []orm.QueryOption) ([]*user, error) {
			return nil, nil
		},
		Count: func(ctx context.Context, _ []orm.QueryOption) (int64, error) {
			return 0, errors.New("boom: SELECT count(*)")
		},
		EntityID: func(u *user) string { return u.ID },
		Pack: func(items []*user, tok string, total int64) (*listResp, error) {
			return &listResp{Users: items, NextPageToken: tok, TotalCount: total}, nil
		},
	})
	_, err := h(context.Background(), connect.NewRequest(&listReq{}))
	cerr := new(connect.Error)
	if !errors.As(err, &cerr) || cerr.Code() != connect.CodeInternal {
		t.Fatalf("count error should be Internal, got %v", err)
	}
	if strings.Contains(cerr.Message(), "SELECT") {
		t.Fatalf("count SQL text leaked: %q", cerr.Message())
	}
}

// --- reason totality across the handler-level error paths -------------

// TestHandlerErrors_CarryReason covers the classified errors this package
// raises BEFORE (or beside) a repository call — the ones built directly
// with connect.NewError rather than routed through mapRepoErr. They are
// just as much a client contract as a SQLSTATE mapping is, and a frontend
// switching on `error.reason` must not fall through to null on any of
// them. Together with TestMapRepoErr_ConstraintViolations this is the
// totality claim: every error crud puts on the wire names its cause.
func TestHandlerErrors_CarryReason(t *testing.T) {
	ctx := context.Background()
	listOp := func() ListOp[listReq, listResp, *user] {
		return ListOp[listReq, listResp, *user]{
			EntityLower:   "user",
			PkColumnName:  "id",
			Columns:       []string{"id", "name"},
			HasPagination: true,
			HasOrderBy:    true,
			OrderBy:       func(_ context.Context, r *listReq) (string, bool) { return r.OrderBy, r.Descending },
			PageToken:     func(r *listReq) string { return r.PageToken },
			PageSize:      func(r *listReq) int { return r.PageSize },
			Query:         func(context.Context, []orm.QueryOption) ([]*user, error) { return nil, nil },
			EntityID:      func(u *user) string { return u.ID },
			Pack:          func([]*user, string, int64) (*listResp, error) { return &listResp{}, nil },
		}
	}

	cases := []struct {
		name       string
		call       func() error
		wantCode   connect.Code
		wantReason string
	}{
		{
			name: "update request missing the entity",
			call: func() error {
				h := HandleUpdate(UpdateOp[updateReq, updateResp, *user]{
					EntityLower:    "user",
					EntityFieldLow: "user",
					Entity:         func(_ context.Context, r *updateReq) (*user, error) { return r.User, entityOrRequired(r.User) },
					Persist:        func(context.Context, *user, ...orm.QueryOption) error { return nil },
					Pack:           func(*user) (*updateResp, error) { return &updateResp{}, nil },
				})
				_, err := h(ctx, connect.NewRequest(&updateReq{}))
				return err
			},
			wantCode:   connect.CodeInvalidArgument,
			wantReason: ReasonRequiredFieldMissing,
		},
		{
			name: "update_mask received but masked persistence unwired",
			call: func() error {
				h := HandleUpdate(UpdateOp[updateReq, updateResp, *user]{
					EntityLower:    "user",
					EntityFieldLow: "user",
					Entity:         func(_ context.Context, r *updateReq) (*user, error) { return r.User, entityOrRequired(r.User) },
					Mask:           func(r *updateReq) []string { return r.Mask },
					Persist:        func(context.Context, *user, ...orm.QueryOption) error { return nil },
					Pack:           func(u *user) (*updateResp, error) { return &updateResp{User: u}, nil },
				})
				_, err := h(ctx, connect.NewRequest(&updateReq{User: &user{ID: "u1"}, Mask: []string{"name"}}))
				return err
			},
			wantCode:   connect.CodeInternal,
			wantReason: ReasonInternal,
		},
		{
			name: "undecodable page token",
			call: func() error {
				_, err := HandleList(listOp())(ctx, connect.NewRequest(&listReq{PageToken: "%%not-base64"}))
				return err
			},
			wantCode:   connect.CodeInvalidArgument,
			wantReason: ReasonInvalidPageToken,
		},
		{
			name: "order_by names an undeclared column",
			call: func() error {
				_, err := HandleList(listOp())(ctx, connect.NewRequest(&listReq{OrderBy: "no_such_column"}))
				return err
			},
			wantCode:   connect.CodeInvalidArgument,
			wantReason: ReasonInvalidOrderBy,
		},
		{
			name: "page_token combined with a non-keyset order",
			call: func() error {
				_, err := HandleList(listOp())(ctx, connect.NewRequest(&listReq{
					PageToken: orm.EncodeCursor("u1"),
					OrderBy:   "name",
				}))
				return err
			},
			wantCode:   connect.CodeInvalidArgument,
			wantReason: ReasonPageTokenOrderConflict,
		},
		{
			name: "response projection failed",
			call: func() error {
				h := HandleGet(GetOp[getReq, getResp, *user]{
					EntityLower: "user",
					ID:          func(r *getReq) string { return r.ID },
					Fetch:       func(context.Context, string, ...orm.QueryOption) (*user, error) { return &user{ID: "u1"}, nil },
					Pack: func(*user) (*getResp, error) {
						return nil, errors.New(`corrupt enum value "LEGACY" for column status`)
					},
				})
				_, err := h(ctx, connect.NewRequest(&getReq{ID: "u1"}))
				return err
			},
			wantCode:   connect.CodeInternal,
			wantReason: ReasonInternal,
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			err := tc.call()
			cerr := new(connect.Error)
			if !errors.As(err, &cerr) {
				t.Fatalf("want a connect.Error, got %T: %v", err, err)
			}
			if cerr.Code() != tc.wantCode {
				t.Fatalf("code = %v, want %v", cerr.Code(), tc.wantCode)
			}
			got := cerr.Meta().Get(svcerr.ReasonHeader)
			if got == "" {
				t.Fatalf("no %s: a frontend switching on error.reason falls through to null here", svcerr.ReasonHeader)
			}
			if got != tc.wantReason {
				t.Fatalf("%s = %q, want %q", svcerr.ReasonHeader, got, tc.wantReason)
			}
		})
	}
}

// entityOrRequired is the test-side spelling of what the generated update
// closure does: an absent entity is ErrEntityRequired, anything else is nil.
func entityOrRequired(u *user) error {
	if u == nil {
		return ErrEntityRequired
	}
	return nil
}
