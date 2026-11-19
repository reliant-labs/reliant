package crud

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"connectrpc.com/connect"
	connectv2 "connectrpc.com/connect/v2"
	"connectrpc.com/connect/v2/connecthttp"
	"google.golang.org/protobuf/types/known/wrapperspb"

	"github.com/reliant-labs/forge/pkg/orm"
	"github.com/reliant-labs/forge/pkg/svcerr"
)

// The connect-neutral lifecycles are the cores the Handle* functions wrap:
// these tests drive them directly, in the shape a connect-go v2 handler
// method has, and then over a real v2 server.

func TestCreate_Neutral(t *testing.T) {
	h := Create(CreateOp[createReq, createResp, *user]{
		EntityLower: "user",
		Entity:      func(_ context.Context, r *createReq) (*user, error) { return &user{Name: r.Name}, nil },
		Persist:     func(_ context.Context, e *user) error { e.ID = "u1"; return nil },
		Pack:        func(e *user) (*createResp, error) { return &createResp{User: e}, nil },
	})
	resp, err := h(context.Background(), &createReq{Name: "Ada"})
	if err != nil || resp.User.ID != "u1" || resp.User.Name != "Ada" {
		t.Fatalf("Create = %+v, %v", resp, err)
	}

	failing := Create(CreateOp[createReq, createResp, *user]{
		EntityLower: "user",
		Entity:      func(context.Context, *createReq) (*user, error) { return &user{}, nil },
		Persist:     func(context.Context, *user) error { return errors.New("db down") },
		Pack:        func(*user) (*createResp, error) { return &createResp{}, nil },
	})
	_, err = failing(context.Background(), &createReq{})
	if svcerr.CodeV2(err) != connectv2.CodeInternal || errMessage(err) != "create user failed" {
		t.Fatalf("Create repo failure = %v (code %v)", err, svcerr.CodeV2(err))
	}
}

func TestGet_Neutral(t *testing.T) {
	want := &user{ID: "u1"}
	h := Get(GetOp[getReq, getResp, *user]{
		EntityLower: "user",
		ID:          func(r *getReq) string { return r.ID },
		Fetch: func(_ context.Context, id string, _ ...orm.QueryOption) (*user, error) {
			if id != "u1" {
				return nil, orm.ErrNoRows
			}
			return want, nil
		},
		Pack: func(u *user) (*getResp, error) { return &getResp{User: u}, nil },
	})
	resp, err := h(context.Background(), &getReq{ID: "u1"})
	if err != nil || resp.User != want {
		t.Fatalf("Get = %+v, %v", resp, err)
	}
	_, err = h(context.Background(), &getReq{ID: "nope"})
	if svcerr.CodeV2(err) != connectv2.CodeNotFound || errMessage(err) != "user not found" {
		t.Fatalf("Get miss = %v (code %v)", err, svcerr.CodeV2(err))
	}
	if r, _ := svcerr.ReasonOf(err); r != ReasonNotFound {
		t.Fatalf("Get miss reason = %q, want %q", r, ReasonNotFound)
	}
}

func TestUpdate_Neutral(t *testing.T) {
	op := UpdateOp[updateReq, updateResp, *user]{
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
	}
	h := Update(op)
	resp, err := h(context.Background(), &updateReq{User: &user{ID: "u1", Name: "Ada"}})
	if err != nil || resp.User.Name != "Ada" {
		t.Fatalf("Update = %+v, %v", resp, err)
	}
	_, err = h(context.Background(), &updateReq{})
	if svcerr.CodeV2(err) != connectv2.CodeInvalidArgument || errMessage(err) != "update user: user is required" {
		t.Fatalf("Update without entity = %v (code %v)", err, svcerr.CodeV2(err))
	}
}

func TestDelete_Neutral(t *testing.T) {
	h := Delete(DeleteOp[deleteReq, deleteResp]{
		EntityLower: "user",
		ID:          func(r *deleteReq) string { return r.ID },
		Persist: func(_ context.Context, id string, _ ...orm.QueryOption) error {
			if id != "u1" {
				return svcerr.NotFound("user")
			}
			return nil
		},
	})
	resp, err := h(context.Background(), &deleteReq{ID: "u1"})
	if err != nil || resp == nil {
		t.Fatalf("Delete = %+v, %v", resp, err)
	}
	_, err = h(context.Background(), &deleteReq{ID: "nope"})
	if svcerr.CodeV2(err) != connectv2.CodeNotFound {
		t.Fatalf("Delete miss = %v (code %v)", err, svcerr.CodeV2(err))
	}
}

func TestList_Neutral(t *testing.T) {
	rows := []*user{{ID: "a"}, {ID: "b"}, {ID: "c"}}
	h := List(ListOp[listReq, listResp, *user]{
		EntityLower:   "user",
		PkColumnName:  "id",
		HasPagination: true,
		PageSize:      func(r *listReq) int { return r.PageSize },
		PageToken:     func(r *listReq) string { return r.PageToken },
		Query:         func(context.Context, []orm.QueryOption) ([]*user, error) { return rows, nil },
		EntityID:      func(u *user) string { return u.ID },
		Pack: func(items []*user, next string, total int64) (*listResp, error) {
			return &listResp{Users: items, NextPageToken: next, TotalCount: total}, nil
		},
	})
	resp, err := h(context.Background(), &listReq{PageSize: 2})
	if err != nil || len(resp.Users) != 2 || resp.NextPageToken == "" {
		t.Fatalf("List = %+v, %v", resp, err)
	}
	_, err = h(context.Background(), &listReq{PageToken: "%%%"})
	if svcerr.CodeV2(err) != connectv2.CodeInvalidArgument || errMessage(err) != "invalid page token" {
		t.Fatalf("List bad token = %v (code %v)", err, svcerr.CodeV2(err))
	}
}

// TestClosureVerdict_BothMajorsPassThrough pins the shim-closure pass-through
// for a connect error of either major: an application's own verdict keeps
// its code and message, and gets the internal reason only if it had none.
func TestClosureVerdict_BothMajorsPassThrough(t *testing.T) {
	for name, verdict := range map[string]error{
		"v1": connect.NewError(connect.CodePermissionDenied, errors.New("not yours")),
		"v2": connectv2.NewError(connectv2.CodePermissionDenied, "not yours"),
	} {
		t.Run(name, func(t *testing.T) {
			h := Get(GetOp[getReq, getResp, *user]{
				EntityLower: "user",
				ID:          func(r *getReq) string { return r.ID },
				Fetch:       func(context.Context, string, ...orm.QueryOption) (*user, error) { return &user{}, nil },
				Pack:        func(*user) (*getResp, error) { return nil, verdict },
			})
			_, err := h(context.Background(), &getReq{})
			if svcerr.CodeV2(err) != connectv2.CodePermissionDenied || errMessage(err) != "not yours" {
				t.Fatalf("closure verdict became %v (code %v)", err, svcerr.CodeV2(err))
			}
			var ce *connect.Error
			if !errors.As(err, &ce) || ce.Meta().Get(svcerr.ReasonHeader) != ReasonInternal {
				t.Fatalf("reason metadata = %v, want %q stamped on an unreasoned verdict", err, ReasonInternal)
			}
		})
	}
}

func errMessage(err error) string {
	var ce *connect.Error
	if errors.As(err, &ce) {
		return ce.Message()
	}
	return ""
}

// stringGetOp is a Get over proto messages, so it can be served for real.
func stringGetOp() GetOp[wrapperspb.StringValue, wrapperspb.StringValue, string] {
	return GetOp[wrapperspb.StringValue, wrapperspb.StringValue, string]{
		EntityLower: "user",
		ID:          func(r *wrapperspb.StringValue) string { return r.GetValue() },
		Fetch: func(_ context.Context, id string, _ ...orm.QueryOption) (string, error) {
			if id != "u1" {
				return "", orm.ErrNoRows
			}
			return "Ada", nil
		},
		Pack: func(name string) (*wrapperspb.StringValue, error) { return wrapperspb.String(name), nil },
	}
}

const getProcedure = "/crud.v1.Users/GetUser"

// TestGet_OverV2ServerMatchesV1 serves the same op through crud.Get on a
// connect-go v2 server (behind svcerr.InterceptorV2, as forge's v2 chains
// install it) and through crud.HandleGet on a v1 server (behind
// svcerr.Interceptor), and requires the same response for a hit and the same
// error — status, JSON body and reason header — for a miss.
func TestGet_OverV2ServerMatchesV1(t *testing.T) {
	get := Get(stringGetOp())
	spec := connectv2.Spec{StreamType: connectv2.StreamTypeUnary, Procedure: getProcedure}
	server := connectv2.NewServer(svcerr.InterceptorV2())
	server.Register(connectv2.Method{Spec: spec, Handler: func(ctx context.Context, _ connectv2.Spec, stream connectv2.ServerStream) error {
		var req wrapperspb.StringValue
		if err := stream.Receive(&req); err != nil {
			return err
		}
		res, err := get(ctx, &req)
		if err != nil {
			return err
		}
		return stream.Send(res)
	}})
	muxV2 := http.NewServeMux()
	connecthttp.Mount(muxV2, server)
	v2 := httptest.NewServer(muxV2)
	t.Cleanup(v2.Close)

	muxV1 := http.NewServeMux()
	muxV1.Handle(getProcedure, connect.NewUnaryHandler(getProcedure, HandleGet(stringGetOp()),
		connect.WithInterceptors(svcerr.Interceptor())))
	v1 := httptest.NewServer(muxV1)
	t.Cleanup(v1.Close)

	client := connectv2.NewClient(connecthttp.NewTransport(v2.Client(), v2.URL))
	var res wrapperspb.StringValue
	if err := client.CallUnary(context.Background(), spec, wrapperspb.String("u1"), &res); err != nil || res.GetValue() != "Ada" {
		t.Fatalf("v2 Get hit = %q, %v", res.GetValue(), err)
	}

	for _, body := range []string{`"u1"`, `"nope"`} {
		a := postJSON(t, v1.URL+getProcedure, body)
		b := postJSON(t, v2.URL+getProcedure, body)
		if a.status != b.status || !bytes.Equal(a.body, b.body) || a.reason != b.reason {
			t.Fatalf("request %s:\n v1 %d %s reason=%q\n v2 %d %s reason=%q", body, a.status, a.body, a.reason, b.status, b.body, b.reason)
		}
	}
	if miss := postJSON(t, v2.URL+getProcedure, `"nope"`); miss.status != http.StatusNotFound || miss.reason != ReasonNotFound {
		t.Fatalf("v2 miss = %d reason=%q, want 404 %q", miss.status, miss.reason, ReasonNotFound)
	}
}

type jsonResponse struct {
	status int
	body   []byte
	reason string
}

func postJSON(t *testing.T, url, body string) jsonResponse {
	t.Helper()
	resp, err := http.Post(url, "application/json", bytes.NewReader([]byte(body)))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(resp.Body)
	if err != nil {
		t.Fatal(err)
	}
	return jsonResponse{status: resp.StatusCode, body: data, reason: resp.Header.Get(svcerr.ReasonHeader)}
}
