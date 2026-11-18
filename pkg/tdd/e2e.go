package tdd

import (
	"net/http/httptest"
	"testing"
)

// E2EClient takes an httptest.Server and a typed-client factory and
// returns a client wired to the server's URL. Cleanup of the server is
// registered via t.Cleanup, so the caller does not need to close it.
//
// Typical usage with a generated Connect client:
//
//	client := tdd.E2EClient(t, srv, func(url string) myv1connect.MyServiceClient {
//	    return myv1connect.NewMyServiceClient(http.DefaultClient, url)
//	})
//
// The factory receives the live server URL (including scheme and port).
func E2EClient[Client any](t *testing.T, srv *httptest.Server, newClient func(url string) Client) Client {
	t.Helper()
	t.Cleanup(srv.Close)
	return newClient(srv.URL)
}
