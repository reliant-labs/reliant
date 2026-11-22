package servicecatalog

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"testing"

	"connectrpc.com/connect"
	"google.golang.org/protobuf/types/known/emptypb"
)

type generatedClient interface {
	Ping(context.Context, *connect.Request[emptypb.Empty]) (*connect.Response[emptypb.Empty], error)
}
type client struct {
	rpc *connect.Client[emptypb.Empty, emptypb.Empty]
}

func (c *client) Ping(ctx context.Context, req *connect.Request[emptypb.Empty]) (*connect.Response[emptypb.Empty], error) {
	return c.rpc.CallUnary(ctx, req)
}
func constructor(h connect.HTTPClient, url string, opts ...connect.ClientOption) generatedClient {
	return &client{connect.NewClient[emptypb.Empty, emptypb.Empty](h, url+"/demo.v1.API/Ping", opts...)}
}

type recordingHTTP struct {
	next     http.Client
	requests int
}

func (h *recordingHTTP) Do(req *http.Request) (*http.Response, error) {
	h.requests++
	return h.next.Do(req)
}

func TestNewClientPreservesGeneratedInterfaceHTTPClientAndOptions(t *testing.T) {
	calls := 0
	mux := http.NewServeMux()
	mux.Handle("/demo.v1.API/Ping", connect.NewUnaryHandler("/demo.v1.API/Ping", func(ctx context.Context, req *connect.Request[emptypb.Empty]) (*connect.Response[emptypb.Empty], error) {
		if req.Header().Get("Authorization") != "Bearer app-owned" || req.Header().Get("X-Order") != "first,second" {
			t.Errorf("auth/options lost: %v", req.Header())
		}
		calls++
		return connect.NewResponse(&emptypb.Empty{}), nil
	}))
	server := httptest.NewServer(mux)
	defer server.Close()
	h := &recordingHTTP{}
	interceptor := func(word string) connect.Interceptor {
		return connect.UnaryInterceptorFunc(func(next connect.UnaryFunc) connect.UnaryFunc {
			return func(ctx context.Context, r connect.AnyRequest) (connect.AnyResponse, error) {
				r.Header().Set("Authorization", "Bearer app-owned")
				prior := r.Header().Get("X-Order")
				if prior != "" {
					prior += ","
				}
				r.Header().Set("X-Order", prior+word)
				return next(ctx, r)
			}
		})
	}
	var c generatedClient = NewClient(ClientBinding{Service: "demo.v1.API", ConfigField: "demo.config.API.url"}, constructor, h, server.URL, connect.WithInterceptors(interceptor("first"), interceptor("second")))
	if _, err := c.Ping(context.Background(), connect.NewRequest(&emptypb.Empty{})); err != nil {
		t.Fatal(err)
	}
	if calls != 1 || h.requests != 1 {
		t.Fatalf("custom HTTPClient not preserved: %d %d", calls, h.requests)
	}
}

func TestDecodeExampleAndUnsafePaths(t *testing.T) {
	raw, err := os.ReadFile("testdata/example.json")
	if err != nil {
		t.Fatal(err)
	}
	c, err := Decode(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(c.Services) != 1 {
		t.Fatal("example schema lost")
	}
	for _, s := range []Source{{Path: "/home/user/app.go", Line: 1}, {Path: "../app.go", Line: 1}, {Path: "C:\\app.go", Line: 1}} {
		c.Processes[0].Source = s
		b, _ := json.Marshal(c)
		if _, err := Decode(b); err == nil {
			t.Fatalf("accepted source %+v", s)
		}
	}
}
