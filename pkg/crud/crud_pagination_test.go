package crud

import (
	"context"
	"errors"
	"strings"
	"testing"

	"connectrpc.com/connect"

	"github.com/reliant-labs/forge/pkg/orm"
)

// paginatedOrderOp builds a List handler with cursor pagination + order_by
// wired, and a repo that returns `rows` (used to exercise the trim/token
// logic under different orders).
func paginatedOrderOp(rows []*user) func(context.Context, *connect.Request[listReq]) (*connect.Response[listResp], error) {
	return HandleList(ListOp[listReq, listResp, *user]{
		EntityLower:   "user",
		PkColumnName:  "id",
		Columns:       []string{"id", "name", "created_at"},
		HasPagination: true,
		HasOrderBy:    true,
		PageToken:     func(r *listReq) string { return r.PageToken },
		PageSize:      func(r *listReq) int { return r.PageSize },
		OrderBy:       func(_ context.Context, r *listReq) (string, bool) { return r.OrderBy, r.Descending },
		Query: func(ctx context.Context, _ []orm.QueryOption) ([]*user, error) {
			return rows, nil
		},
		EntityID: func(u *user) string { return u.ID },
		Pack: func(items []*user, tok string, _ int64) (*listResp, error) {
			return &listResp{Users: items, NextPageToken: tok}, nil
		},
	})
}

// A page_token combined with an order_by on a NON-PK column is the dup/skip
// bug: the cursor pages by `pk > cursor` while the rows are sorted by another
// column. The library must REJECT it loudly instead of returning wrong pages.
func TestHandleList_NonPKOrderByWithToken_Rejected(t *testing.T) {
	// A valid PK cursor token (as the previous page would have minted).
	token := orm.EncodeCursor("uZ")
	h := paginatedOrderOp(nil)
	_, err := h(context.Background(), connect.NewRequest(&listReq{
		OrderBy:   "created_at",
		PageToken: token,
	}))
	cerr := new(connect.Error)
	if !errors.As(err, &cerr) || cerr.Code() != connect.CodeInvalidArgument {
		t.Fatalf("non-PK order_by + page_token must be InvalidArgument, got %v", err)
	}
	if !strings.Contains(cerr.Message(), "page_token") || !strings.Contains(cerr.Message(), "id") {
		t.Fatalf("rejection should name the incompatibility and the PK column: %q", cerr.Message())
	}
}

// PK-DESC also breaks the ascending `pk > cursor` predicate, so a token with
// descending PK order is rejected too.
func TestHandleList_PKDescWithToken_Rejected(t *testing.T) {
	h := paginatedOrderOp(nil)
	_, err := h(context.Background(), connect.NewRequest(&listReq{
		OrderBy:    "id",
		Descending: true,
		PageToken:  orm.EncodeCursor("uZ"),
	}))
	cerr := new(connect.Error)
	if !errors.As(err, &cerr) || cerr.Code() != connect.CodeInvalidArgument {
		t.Fatalf("PK DESC + page_token must be InvalidArgument, got %v", err)
	}
}

// First page of a non-PK-ordered list (no token) is allowed, but NO next-page
// token is minted — ordered lists are single-page until composite cursors
// land. This is what makes dup/skip impossible: a client is never handed a
// cursor it would replay against a differently-sorted set.
func TestHandleList_NonPKOrderBy_NoTokenMinted(t *testing.T) {
	// 51 rows; default page size 50 → the extra row proves there IS a next
	// page, yet no token is minted for a non-PK order.
	rows := make([]*user, 51)
	for i := range rows {
		rows[i] = &user{ID: "u" + string(rune('A'+i%26))}
	}
	h := paginatedOrderOp(rows)
	resp, err := h(context.Background(), connect.NewRequest(&listReq{OrderBy: "created_at"}))
	if err != nil {
		t.Fatalf("first page of an ordered list should be allowed: %v", err)
	}
	if len(resp.Msg.Users) != 50 {
		t.Fatalf("expected trim to 50 rows, got %d", len(resp.Msg.Users))
	}
	if resp.Msg.NextPageToken != "" {
		t.Fatalf("non-PK order must not mint a page token (unusable cursor); got %q", resp.Msg.NextPageToken)
	}
}

// Ordering by the PK ascending IS keyset-safe: pagination proceeds and a
// token is minted, exactly as un-ordered pagination does.
func TestHandleList_PKOrderByAsc_TokenMinted(t *testing.T) {
	rows := make([]*user, 51)
	for i := range rows {
		rows[i] = &user{ID: "u" + string(rune('A'+i%26))}
	}
	// Explicit PK-ASC order with a valid cursor: honored, not rejected.
	h := paginatedOrderOp(rows)
	resp, err := h(context.Background(), connect.NewRequest(&listReq{
		OrderBy:   "id",
		PageToken: orm.EncodeCursor("u0"),
	}))
	if err != nil {
		t.Fatalf("PK-ASC order + token is keyset-safe, should be honored: %v", err)
	}
	if resp.Msg.NextPageToken == "" {
		t.Fatal("PK-ASC ordered pagination should mint a next-page token")
	}
}

func TestOrderKeysetSafe(t *testing.T) {
	cases := []struct {
		clause     string
		descending bool
		pk         string
		want       bool
	}{
		{"", false, "id", true},                // default PK ASC
		{"id", false, "id", true},              // explicit PK ASC
		{"id ASC", false, "id", true},          // explicit direction token
		{"ID", false, "id", true},              // case-insensitive
		{"id", true, "id", false},              // PK DESC (req.Descending)
		{"id DESC", false, "id", false},        // PK DESC (token)
		{"created_at", false, "id", false},     // non-PK column
		{"name", false, "id", false},           // non-PK column
		{"id, created_at", false, "id", false}, // composite clause
		{"", false, "", false},                 // PK-cursor disabled
	}
	for _, tc := range cases {
		if got := orderKeysetSafe(tc.clause, tc.descending, tc.pk); got != tc.want {
			t.Errorf("orderKeysetSafe(%q, %v, %q) = %v, want %v", tc.clause, tc.descending, tc.pk, got, tc.want)
		}
	}
}
