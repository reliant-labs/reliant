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

// Unsupported orders must not silently hide additional rows. Exercise both
// the default page size and Growth's 101-record / 100-row maximum-page case.
// No Count callback is wired: correctness must depend on fetch+1, not totals.
func TestHandleList_UnsupportedOrderOverflow_Rejected(t *testing.T) {
	for _, order := range []struct {
		name, clause string
		desc         bool
	}{
		{"name", "name", false},
		{"created_at", "created_at", false},
		{"pk_desc", "id", true},
		{"pk_desc_clause", "id DESC", false},
	} {
		for _, size := range []int{0, 100, 1000} {
			t.Run(fmt.Sprintf("%s/size_%d", order.name, size), func(t *testing.T) {
				limit := 100
				if size == 0 {
					limit = 50
				}
				h := paginatedOrderOp(make([]*user, limit+1))
				resp, err := h(context.Background(), connect.NewRequest(&listReq{
					OrderBy: order.clause, Descending: order.desc, PageSize: size,
				}))
				cerr := new(connect.Error)
				if !errors.As(err, &cerr) || cerr.Code() != connect.CodeInvalidArgument {
					t.Fatalf("overflow must be InvalidArgument, got response %v, error %v", resp, err)
				}
				if resp != nil {
					t.Fatal("must not return a truncated success response")
				}
				if got := cerr.Meta().Get(svcerr.ReasonHeader); got != ReasonPageTokenOrderConflict {
					t.Errorf("reason = %q, want %q", got, ReasonPageTokenOrderConflict)
				}
				for _, want := range []string{"page_size", "order_by", "id", "ascending", "sort locally"} {
					if !strings.Contains(cerr.Message(), want) {
						t.Errorf("rejection should mention %q: %q", want, cerr.Message())
					}
				}
			})
		}
	}
}

// Preserve existing small sorted lists, including a full page with no extra
// row. They need no continuation cursor and must remain successful.
func TestHandleList_UnsupportedOrderFitsOnePage(t *testing.T) {
	for _, count := range []int{0, 49, 50} {
		t.Run(fmt.Sprint(count), func(t *testing.T) {
			rows := make([]*user, count)
			h := paginatedOrderOp(rows)
			resp, err := h(context.Background(), connect.NewRequest(&listReq{OrderBy: "name"}))
			if err != nil {
				t.Fatalf("single-page sort should be allowed: %v", err)
			}
			if len(resp.Msg.Users) != count || resp.Msg.NextPageToken != "" {
				t.Fatalf("expected all %d rows and no token, got %+v", count, resp.Msg)
			}
		})
	}
}

// Growth's generated response carries total_count. The same 101-row dataset
// must fail explicitly under name ordering but remain reachable in ID order.
func TestHandleList_101RowsWithTotalCount(t *testing.T) {
	rows := make([]*user, 101)
	for i := range rows {
		rows[i] = &user{ID: fmt.Sprintf("u%03d", i)}
	}
	h := HandleList(ListOp[listReq, listResp, *user]{
		EntityLower: "user", PkColumnName: "id", Columns: []string{"id", "name"},
		HasPagination: true, HasOrderBy: true,
		PageToken: func(r *listReq) string { return r.PageToken },
		PageSize:  func(r *listReq) int { return r.PageSize },
		OrderBy:   func(_ context.Context, r *listReq) (string, bool) { return r.OrderBy, r.Descending },
		Query:     func(context.Context, []orm.QueryOption) ([]*user, error) { return rows, nil },
		Count:     func(context.Context, []orm.QueryOption) (int64, error) { return 101, nil },
		EntityID:  func(u *user) string { return u.ID },
		Pack: func(items []*user, tok string, total int64) (*listResp, error) {
			return &listResp{Users: items, NextPageToken: tok, TotalCount: total}, nil
		},
	})
	resp, err := h(context.Background(), connect.NewRequest(&listReq{PageSize: 100, OrderBy: "name"}))
	if connect.CodeOf(err) != connect.CodeInvalidArgument || resp != nil {
		t.Fatalf("name order must not return 100 rows without continuation: response %v, error %v", resp, err)
	}
	first, err := h(context.Background(), connect.NewRequest(&listReq{PageSize: 100, OrderBy: "id"}))
	if err != nil {
		t.Fatal(err)
	}
	if len(first.Msg.Users) != 100 || first.Msg.TotalCount != 101 || first.Msg.NextPageToken == "" {
		t.Fatalf("ID order must provide the continuation and total: %+v", first.Msg)
	}
	cursor, err := orm.DecodeCursor(first.Msg.NextPageToken)
	if err != nil || cursor != "u099" {
		t.Fatalf("cursor = %q, %v; want last returned ID u099", cursor, err)
	}
	// Model the repository's existing pk > cursor result for the next page.
	rows = rows[100:]
	last, err := h(context.Background(), connect.NewRequest(&listReq{
		PageSize: 100, OrderBy: "id", PageToken: first.Msg.NextPageToken,
	}))
	if err != nil {
		t.Fatal(err)
	}
	if len(last.Msg.Users) != 1 || last.Msg.Users[0].ID != "u100" || last.Msg.TotalCount != 101 || last.Msg.NextPageToken != "" {
		t.Fatalf("last ID-ordered page = %+v", last.Msg)
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
