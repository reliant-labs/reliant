package testkit_test

import (
	"net/http"
	"strings"
	"testing"

	"github.com/reliant-labs/forge/pkg/testkit"
)

// NewConnectClientV2's server answers an unknown method of its service with
// connect-go v1's plain 404, as production's MuxV2 does, so a test asserting
// the 404 a version-skewed client relies on passes against it.
func TestNewConnectClientV2_UnknownMethodIsNotFound(t *testing.T) {
	t.Parallel()
	srv, _ := testkit.NewConnectClientV2(t, kindRegisterV2, clientV2)
	missing := kindSpecV2.Procedure[:strings.LastIndexByte(kindSpecV2.Procedure, '/')+1] + "Missing"
	res, err := srv.Client().Post(srv.URL+missing, "application/json", strings.NewReader("{}"))
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = res.Body.Close() }()
	if res.StatusCode != http.StatusNotFound || !strings.HasPrefix(res.Header.Get("Content-Type"), "text/plain") {
		t.Fatalf("unknown method: %d %s, want a plain 404", res.StatusCode, res.Header.Get("Content-Type"))
	}
}
